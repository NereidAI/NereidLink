// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// runner.go runs the background escalation loop. Each tick it (1) cancels
// pending steps blocked by an ack/clear, (2) materializes pending firing
// state for live alarms bound to enabled chains, and (3) atomically
// claims due steps and delivers them. Claiming and marking a step in a
// single UPDATE guarantees at-most-once firing even if delivery fails or
// the process restarts mid-tick.

package escalation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultInterval bounds the delay between escalation sweeps.
const defaultInterval = 10 * time.Second

// InboxRecord carries the fields of an escalation in-app notification.
type InboxRecord struct {
	// TenantID owns the notification.
	TenantID string

	// AlarmID links the originating alarm.
	AlarmID string

	// DeviceID links the originating device.
	DeviceID string

	// Severity mirrors the alarm severity.
	Severity string

	// Title is the notification title.
	Title string

	// Body is the notification text.
	Body string
}

// Inbox records an in-app notification for one fired step. It is
// implemented by an adapter around notify.Inbox in the cmd package.
type Inbox interface {
	// Record persists and publishes one notification.
	Record(ctx context.Context, in InboxRecord) error
}

// Runner is the background escalation worker.
type Runner struct {
	pool     *pgxpool.Pool
	sender   Sender
	inbox    Inbox
	interval time.Duration
}

// NewRunner constructs the escalation runner. A non-positive interval
// falls back to the default sweep interval.
func NewRunner(pool *pgxpool.Pool, sender Sender, inbox Inbox, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Runner{pool: pool, sender: sender, inbox: inbox, interval: interval}
}

// Run executes one sweep immediately, then on every tick until ctx ends.
func (r *Runner) Run(ctx context.Context) error {
	run := func() {
		if err := r.sweep(ctx); err != nil {
			slog.Warn("escalation sweep failed", "error", err)
		}
	}
	run()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			run()
		}
	}
}

// sweep performs materialize, cancel and fire phases in order.
// Materializing before canceling lets one tick handle an alarm that was
// already acked/clear before its firing state existed.
func (r *Runner) sweep(ctx context.Context) error {
	if err := r.materialize(ctx); err != nil {
		return err
	}
	if err := r.cancelBlocked(ctx); err != nil {
		return err
	}
	if err := r.fireDue(ctx); err != nil {
		return err
	}
	// Repeated reminders are an independent loop over still-ACTIVE
	// alarms; they stop once the alarm is acked or cleared.
	if err := r.materializeReminders(ctx); err != nil {
		return err
	}
	if err := r.cancelReminders(ctx); err != nil {
		return err
	}
	return r.fireDueReminders(ctx)
}

// cancelBlocked cancels pending steps whose stop flags match the alarm
// lifecycle state.
func (r *Runner) cancelBlocked(ctx context.Context) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE alarm_escalation_state AS st
		 SET state = 'CANCELED'
		 FROM alarm AS a, escalation_step AS s
		 WHERE st.alarm_id = a.id AND st.step_id = s.id
		   AND st.state = 'PENDING'
		   AND ((a.state = 'ACK' AND s.stop_on_ack)
		        OR (a.state = 'CLEARED' AND s.stop_on_clear))`)
	if err != nil {
		return fmt.Errorf("cancel blocked steps: %w", err)
	}
	return nil
}

// materialize creates pending state for every step of every live alarm
// bound to an enabled chain that does not have state yet. Fire time is
// the alarm start plus the step delay.
func (r *Runner) materialize(ctx context.Context) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO alarm_escalation_state
		    (alarm_id, step_id, state, fire_at)
		 SELECT a.id, s.id, 'PENDING',
		        a.started_at + make_interval(secs => s.delay_seconds)
		 FROM alarm AS a
		 JOIN escalation_chain AS c ON c.id = a.escalation_chain_id
		 JOIN escalation_step AS s ON s.chain_id = c.id
		 WHERE a.state IN ('ACTIVE', 'ACK') AND c.enabled
		 ON CONFLICT DO NOTHING`)
	if err != nil {
		return fmt.Errorf("materialize steps: %w", err)
	}
	return nil
}

// dueStep is a claimed step joined with its alarm and chain.
type dueStep struct {
	alarmID         string
	tenantID        string
	deviceID        string
	alarmType       string
	severity        string
	startedAt       time.Time
	targetRole      string
	targetGroupID   string
	targetGroupName string
	chainName       string
	order           int
}

// fireDue atomically claims due pending steps and delivers each.
func (r *Runner) fireDue(ctx context.Context) error {
	rows, err := r.pool.Query(ctx,
		`UPDATE alarm_escalation_state
		 SET state = 'FIRED', fired_at = now()
		 WHERE state = 'PENDING' AND fire_at <= now()
		 RETURNING alarm_id::text, step_id::text`)
	if err != nil {
		return fmt.Errorf("claim due steps: %w", err)
	}

	type claimed struct{ alarmID, stepID string }
	claimedRows := []claimed{}
	for rows.Next() {
		var entry claimed
		if err := rows.Scan(&entry.alarmID, &entry.stepID); err != nil {
			rows.Close()
			return fmt.Errorf("scan claimed step: %w", err)
		}
		claimedRows = append(claimedRows, entry)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate claimed steps: %w", err)
	}
	rows.Close()
	for _, entry := range claimedRows {
		if err := r.deliver(ctx, entry.alarmID, entry.stepID); err != nil {
			slog.Warn("escalation delivery failed", "error", err)
		}
	}
	return nil
}

// deliver loads the claimed step context, resolves recipients and sends
// the external and in-app notifications.
func (r *Runner) deliver(ctx context.Context, alarmID, stepID string) error {
	due, err := r.loadDue(ctx, alarmID, stepID)
	if err != nil {
		return err
	}
	recipients, err := r.roleScopedEmails(ctx, due.tenantID, due.targetRole, due.targetGroupID)
	if err != nil {
		return err
	}

	target := due.targetRole
	scopedTo := ""
	if due.targetGroupID != "" {
		target = fmt.Sprintf("%s in group %q", due.targetRole, due.targetGroupName)
		scopedTo = fmt.Sprintf("\nScoped to device group %q.", due.targetGroupName)
	}
	subject := fmt.Sprintf("[%s] %s escalation: %s",
		due.severity, due.alarmType, target)
	body := fmt.Sprintf(
		"Alarm %s (%s) on device %s started at %s.\n"+
			"Escalation chain %q step %d notified role %s.%s",
		due.alarmID, due.alarmType, due.deviceID,
		due.startedAt.UTC().Format(time.RFC3339),
		due.chainName, due.order+1, due.targetRole, scopedTo)

	if err := r.sender.Send(ctx, SendInput{
		Recipients: recipients,
		Subject:    subject,
		Body:       body,
		Severity:   due.severity,
	}); err != nil {
		slog.Warn("escalation external send failed", "error", err)
	}
	if r.inbox != nil {
		if err := r.inbox.Record(ctx, InboxRecord{
			TenantID: due.tenantID,
			AlarmID:  due.alarmID,
			DeviceID: due.deviceID,
			Severity: due.severity,
			Title:    subject,
			Body:     body,
		}); err != nil {
			return fmt.Errorf("record inbox: %w", err)
		}
	}
	return nil
}

// loadDue joins a claimed state row with its alarm, step and chain.
func (r *Runner) loadDue(ctx context.Context, alarmID, stepID string) (dueStep, error) {
	var due dueStep
	err := r.pool.QueryRow(ctx,
		`SELECT a.id::text, a.tenant_id::text, a.device_id::text, a.type,
		        a.severity, a.started_at, s.target_role,
		        COALESCE(s.target_group_id::text,''), COALESCE(g.name,''),
		        c.name, s.step_order
		 FROM alarm_escalation_state AS st
		 JOIN alarm AS a ON a.id = st.alarm_id
		 JOIN escalation_step AS s ON s.id = st.step_id
		 JOIN escalation_chain AS c ON c.id = s.chain_id
		 LEFT JOIN device_group AS g ON g.id = s.target_group_id
		 WHERE st.alarm_id = $1 AND st.step_id = $2`,
		alarmID, stepID).Scan(&due.alarmID, &due.tenantID, &due.deviceID,
		&due.alarmType, &due.severity, &due.startedAt, &due.targetRole,
		&due.targetGroupID, &due.targetGroupName, &due.chainName, &due.order)
	if err != nil {
		return dueStep{}, fmt.Errorf("load due step: %w", err)
	}
	return due, nil
}

// roleScopedEmails resolves the active email addresses of tenant members
// holding the given role, optionally narrowed to those whose RBAC data
// scope covers a device group's subtree. An empty group id falls back to
// every holder of the role in the tenant.
func (r *Runner) roleScopedEmails(ctx context.Context, tenantID, role, groupID string) ([]string, error) {
	if strings.TrimSpace(groupID) == "" {
		return r.roleEmails(ctx, tenantID, role)
	}
	rows, err := r.pool.Query(ctx,
		`WITH RECURSIVE subtree AS (
		   SELECT g.id, g.customer_id
		   FROM device_group AS g
		   WHERE g.id = $3::uuid AND g.tenant_id = $1::uuid
		   UNION ALL
		   SELECT s.id, s.customer_id
		   FROM device_group AS s
		   JOIN subtree AS p ON s.parent_id = p.id
		   WHERE s.tenant_id = $1::uuid
		 ),
		 ancestors AS (
		   SELECT g.parent_id AS id
		   FROM device_group AS g
		   WHERE g.id = $3::uuid AND g.tenant_id = $1::uuid
		     AND g.parent_id IS NOT NULL
		   UNION ALL
		   SELECT a.parent_id
		   FROM device_group AS a
		   JOIN ancestors AS an ON a.id = an.id
		   WHERE a.parent_id IS NOT NULL AND a.tenant_id = $1::uuid
		 )
		 SELECT DISTINCT u.email
		 FROM app_user AS u
		 JOIN tenant_membership AS m ON m.user_id = u.id
		 JOIN membership_role AS rl ON rl.membership_id = m.id
		 WHERE m.tenant_id = $1 AND rl.role = $2
		   AND NOT u.disabled AND u.email <> ''
		   AND (
		     m.scope = 'TENANT'
		     OR (m.scope = 'GROUP' AND m.scope_id IN (
		           SELECT id FROM subtree UNION SELECT id FROM ancestors))
		     OR (m.scope = 'CUSTOMER' AND m.scope_id IN (
		           SELECT customer_id FROM subtree WHERE customer_id IS NOT NULL))
		   )`,
		tenantID, role, groupID)
	if err != nil {
		return nil, fmt.Errorf("resolve scoped role emails: %w", err)
	}
	defer rows.Close()

	emails := []string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, fmt.Errorf("scan scoped role email: %w", err)
		}
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scoped role emails: %w", err)
	}
	return emails, nil
}

// roleEmails resolves the active email addresses of tenant members
// holding the given role.
func (r *Runner) roleEmails(ctx context.Context, tenantID, role string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT u.email
		 FROM app_user AS u
		 JOIN tenant_membership AS m ON m.user_id = u.id
		 JOIN membership_role AS r ON r.membership_id = m.id
		 WHERE m.tenant_id = $1 AND r.role = $2
		   AND NOT u.disabled AND u.email <> ''`,
		tenantID, role)
	if err != nil {
		return nil, fmt.Errorf("resolve role emails: %w", err)
	}
	defer rows.Close()

	emails := []string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, fmt.Errorf("scan role email: %w", err)
		}
		emails = append(emails, email)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate role emails: %w", err)
	}
	return emails, nil
}

// materializeReminders seeds a reminder cursor for every ACTIVE alarm
// bound to an enabled chain with a positive repeat interval. The first
// reminder fires one interval after the alarm started.
func (r *Runner) materializeReminders(ctx context.Context) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO alarm_reminder_state (alarm_id, next_fire_at)
		 SELECT a.id,
		        a.started_at + make_interval(secs => c.repeat_interval_seconds)
		 FROM alarm AS a
		 JOIN escalation_chain AS c ON c.id = a.escalation_chain_id
		 WHERE a.state = 'ACTIVE'
		   AND c.enabled AND c.repeat_interval_seconds > 0
		 ON CONFLICT DO NOTHING`)
	if err != nil {
		return fmt.Errorf("materialize reminders: %w", err)
	}
	return nil
}

// cancelReminders deletes cursors once the alarm is no longer ACTIVE,
// so ack/clear stops further reminders on the next sweep.
func (r *Runner) cancelReminders(ctx context.Context) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM alarm_reminder_state AS st
		 USING alarm AS a
		 WHERE st.alarm_id = a.id AND a.state <> 'ACTIVE'`)
	if err != nil {
		return fmt.Errorf("cancel reminders: %w", err)
	}
	return nil
}

// reminderRow is a claimed due reminder joined with its alarm/chain.
type reminderRow struct {
	alarmID         string
	tenantID        string
	deviceID        string
	alarmType       string
	severity        string
	startedAt       time.Time
	targetRole      string
	targetGroupID   string
	targetGroupName string
	chainName       string
	interval        int64
}

// fireDueReminders atomically advances due cursors and delivers each
// reminder. Advancing next_fire_at by the interval (and bumping the
// count) inside the UPDATE makes delivery at-most-once per cadence even
// across restarts; a failed send simply waits for the next sweep where
// the cursor is already due, so no reminder is permanently lost.
func (r *Runner) fireDueReminders(ctx context.Context) error {
	rows, err := r.pool.Query(ctx,
		`UPDATE alarm_reminder_state AS st
		 SET next_fire_at = st.next_fire_at
		       + make_interval(secs => c.repeat_interval_seconds),
		     fired_count = st.fired_count + 1
		 FROM escalation_chain AS c, alarm AS a
		 WHERE st.alarm_id = a.id AND a.escalation_chain_id = c.id
		   AND st.next_fire_at <= now()
		 RETURNING st.alarm_id::text, st.fired_count`)
	if err != nil {
		return fmt.Errorf("claim due reminders: %w", err)
	}
	type claimed struct {
		alarmID string
		count   int
	}
	var claimedRows []claimed
	for rows.Next() {
		var entry claimed
		if err := rows.Scan(&entry.alarmID, &entry.count); err != nil {
			rows.Close()
			return fmt.Errorf("scan claimed reminder: %w", err)
		}
		claimedRows = append(claimedRows, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate claimed reminders: %w", err)
	}
	for _, entry := range claimedRows {
		if err := r.deliverReminder(ctx, entry.alarmID, entry.count); err != nil {
			slog.Warn("escalation reminder delivery failed", "alarm", entry.alarmID, "error", err)
		}
	}
	return nil
}

// deliverReminder loads the claimed reminder context, resolves the
// repeat role recipients and sends the external/in-app notification.
func (r *Runner) deliverReminder(ctx context.Context, alarmID string, count int) error {
	rem, err := r.loadReminder(ctx, alarmID)
	if err != nil {
		return err
	}
	recipients, err := r.roleScopedEmails(ctx, rem.tenantID, rem.targetRole, rem.targetGroupID)
	if err != nil {
		return err
	}
	scopedTo := ""
	if rem.targetGroupID != "" {
		scopedTo = fmt.Sprintf("\nScoped to device group %q.", rem.targetGroupName)
	}
	subject := fmt.Sprintf("[%s] REMINDER #%d: %s still unacknowledged",
		rem.severity, count, rem.alarmType)
	body := fmt.Sprintf(
		"Alarm %s (%s) on device %s started at %s and is still ACTIVE.\n"+
			"Escalation chain %q reminder %d to role %s (every %ds).%s",
		rem.alarmID, rem.alarmType, rem.deviceID,
		rem.startedAt.UTC().Format(time.RFC3339),
		rem.chainName, count, rem.targetRole, rem.interval, scopedTo)

	if err := r.sender.Send(ctx, SendInput{
		Recipients: recipients,
		Subject:    subject,
		Body:       body,
		Severity:   rem.severity,
	}); err != nil {
		slog.Warn("escalation reminder external send failed", "error", err)
	}
	if r.inbox != nil {
		if err := r.inbox.Record(ctx, InboxRecord{
			TenantID: rem.tenantID,
			AlarmID:  rem.alarmID,
			DeviceID: rem.deviceID,
			Severity: rem.severity,
			Title:    subject,
			Body:     body,
		}); err != nil {
			return fmt.Errorf("record reminder inbox: %w", err)
		}
	}
	return nil
}

// loadReminder joins an alarm with its chain to read reminder context.
func (r *Runner) loadReminder(ctx context.Context, alarmID string) (reminderRow, error) {
	var rem reminderRow
	err := r.pool.QueryRow(ctx,
		`SELECT a.id::text, a.tenant_id::text, a.device_id::text, a.type,
		        a.severity, a.started_at, COALESCE(c.repeat_target_role,''),
		        COALESCE(c.repeat_target_group_id::text,''), COALESCE(g.name,''),
		        c.name, c.repeat_interval_seconds
		 FROM alarm AS a
		 JOIN escalation_chain AS c ON c.id = a.escalation_chain_id
		 LEFT JOIN device_group AS g ON g.id = c.repeat_target_group_id
		 WHERE a.id = $1`,
		alarmID).Scan(&rem.alarmID, &rem.tenantID, &rem.deviceID,
		&rem.alarmType, &rem.severity, &rem.startedAt, &rem.targetRole,
		&rem.targetGroupID, &rem.targetGroupName, &rem.chainName, &rem.interval)
	if err != nil {
		return reminderRow{}, fmt.Errorf("load reminder: %w", err)
	}
	return rem, nil
}
