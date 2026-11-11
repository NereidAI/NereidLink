// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// customers.go implements customer and device-group management for the
// ownership model from ADR-0013: tenant-scoped CRUD with parent and
// cycle validation on the group tree.

package registry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ListCustomers returns every customer of a tenant ordered by name.
func (a *Admin) ListCustomers(ctx context.Context, tenantID string) ([]CustomerDTO, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT id::text, name, contact_name, contact_email, external_id, created_at
		 FROM customer
		 WHERE tenant_id = $1
		 ORDER BY name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list customers: %w", err)
	}
	defer rows.Close()

	customers := []CustomerDTO{}
	for rows.Next() {
		var customer CustomerDTO
		var createdAt time.Time
		if err := rows.Scan(&customer.ID, &customer.Name,
			&customer.ContactName, &customer.ContactEmail,
			&customer.ExternalID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan customer: %w", err)
		}
		customer.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		customers = append(customers, customer)
	}
	return customers, rows.Err()
}

// CreateCustomer creates one customer.
func (a *Admin) CreateCustomer(ctx context.Context, tenantID string, input CustomerDTO) (CustomerDTO, error) {
	if err := validateCustomer(input); err != nil {
		return CustomerDTO{}, err
	}
	input.ID = uuid.NewString()
	if _, err := a.pool.Exec(ctx,
		`INSERT INTO customer (id, tenant_id, name, contact_name, contact_email, external_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		input.ID, tenantID, input.Name, input.ContactName,
		input.ContactEmail, input.ExternalID); err != nil {
		return CustomerDTO{}, mapSQLError(err)
	}
	return input, nil
}

// GetCustomer returns one tenant-owned customer.
func (a *Admin) GetCustomer(ctx context.Context, tenantID, customerID string) (CustomerDTO, error) {
	var customer CustomerDTO
	var createdAt time.Time
	err := a.pool.QueryRow(ctx,
		`SELECT id::text, name, contact_name, contact_email, external_id, created_at
		 FROM customer
		 WHERE id = $1 AND tenant_id = $2`,
		customerID, tenantID).Scan(&customer.ID, &customer.Name,
		&customer.ContactName, &customer.ContactEmail,
		&customer.ExternalID, &createdAt)
	if err != nil {
		return CustomerDTO{}, mapSQLError(err)
	}
	customer.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return customer, nil
}

// UpdateCustomer changes the mutable fields of one customer.
func (a *Admin) UpdateCustomer(ctx context.Context, tenantID, customerID string, input CustomerDTO) (CustomerDTO, error) {
	if err := validateCustomer(input); err != nil {
		return CustomerDTO{}, err
	}
	tag, err := a.pool.Exec(ctx,
		`UPDATE customer
		 SET name = $1, contact_name = $2, contact_email = $3,
		     external_id = $4, updated_at = now()
		 WHERE id = $5 AND tenant_id = $6`,
		input.Name, input.ContactName, input.ContactEmail,
		input.ExternalID, customerID, tenantID)
	if err != nil {
		return CustomerDTO{}, mapSQLError(err)
	}
	if tag.RowsAffected() == 0 {
		return CustomerDTO{}, ErrNotFound
	}
	input.ID = customerID
	return input, nil
}

// DeleteCustomer removes one customer. A customer still referenced by
// devices or groups is rejected so ownership is never orphaned.
func (a *Admin) DeleteCustomer(ctx context.Context, tenantID, customerID string) error {
	var referenced bool
	if err := a.pool.QueryRow(ctx,
		`SELECT EXISTS(
		    SELECT 1 FROM device WHERE customer_id = $1
		    UNION ALL
		    SELECT 1 FROM device_group WHERE customer_id = $1)`,
		customerID).Scan(&referenced); err != nil {
		return fmt.Errorf("check customer references: %w", err)
	}
	if referenced {
		return fmt.Errorf("%w: customer is still assigned to devices or groups",
			ErrConflict)
	}

	tag, err := a.pool.Exec(ctx,
		`DELETE FROM customer WHERE id = $1 AND tenant_id = $2`,
		customerID, tenantID)
	if err != nil {
		return mapSQLError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// validateCustomer validates customer fields.
func validateCustomer(input CustomerDTO) error {
	if strings.TrimSpace(input.Name) == "" {
		return invalid("customer name is required")
	}
	if len(input.Name) > 255 {
		return invalid("customer name is too long")
	}
	return nil
}

// ListGroups returns every group of a tenant ordered by name.
func (a *Admin) ListGroups(ctx context.Context, tenantID string) ([]GroupDTO, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT id::text, COALESCE(customer_id::text, ''),
		        COALESCE(parent_id::text, ''), name, created_at
		 FROM device_group
		 WHERE tenant_id = $1
		 ORDER BY name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()

	groups := []GroupDTO{}
	for rows.Next() {
		var group GroupDTO
		var createdAt time.Time
		if err := rows.Scan(&group.ID, &group.CustomerID,
			&group.ParentID, &group.Name, &createdAt); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		group.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// CreateGroup creates one group after validating its customer and parent.
func (a *Admin) CreateGroup(ctx context.Context, tenantID string, input GroupDTO) (GroupDTO, error) {
	if err := a.validateGroup(ctx, tenantID, "", input); err != nil {
		return GroupDTO{}, err
	}
	input.ID = uuid.NewString()
	if _, err := a.pool.Exec(ctx,
		`INSERT INTO device_group (id, tenant_id, customer_id, parent_id, name)
		 VALUES ($1, $2, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid, $5)`,
		input.ID, tenantID, input.CustomerID, input.ParentID, input.Name); err != nil {
		return GroupDTO{}, mapSQLError(err)
	}
	return input, nil
}

// UpdateGroup changes the name, customer anchoring or parent of one group
// and rejects a parent choice that would create a cycle.
func (a *Admin) UpdateGroup(ctx context.Context, tenantID, groupID string, input GroupDTO) error {
	if err := a.validateGroup(ctx, tenantID, groupID, input); err != nil {
		return err
	}
	tag, err := a.pool.Exec(ctx,
		`UPDATE device_group
		 SET customer_id = NULLIF($1,'')::uuid,
		     parent_id = NULLIF($2,'')::uuid,
		     name = $3,
		     updated_at = now()
		 WHERE id = $4 AND tenant_id = $5`,
		input.CustomerID, input.ParentID, input.Name, groupID, tenantID)
	if err != nil {
		return mapSQLError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGroup removes one group. A group with children or assigned
// devices is rejected.
func (a *Admin) DeleteGroup(ctx context.Context, tenantID, groupID string) error {
	var referenced bool
	if err := a.pool.QueryRow(ctx,
		`SELECT EXISTS(
		    SELECT 1 FROM device_group WHERE parent_id = $1
		    UNION ALL
		    SELECT 1 FROM device WHERE group_id = $1)`,
		groupID).Scan(&referenced); err != nil {
		return fmt.Errorf("check group references: %w", err)
	}
	if referenced {
		return fmt.Errorf("%w: group has child groups or assigned devices",
			ErrConflict)
	}

	tag, err := a.pool.Exec(ctx,
		`DELETE FROM device_group WHERE id = $1 AND tenant_id = $2`,
		groupID, tenantID)
	if err != nil {
		return mapSQLError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// validateGroup validates group fields and its customer/parent links.
// selfID is empty on creation and the edited group id on update.
func (a *Admin) validateGroup(ctx context.Context, tenantID, selfID string, input GroupDTO) error {
	if strings.TrimSpace(input.Name) == "" {
		return invalid("group name is required")
	}
	if len(input.Name) > 255 {
		return invalid("group name is too long")
	}
	if input.CustomerID != "" {
		if _, err := a.GetCustomer(ctx, tenantID, input.CustomerID); err != nil {
			return err
		}
	}
	if input.ParentID == "" {
		return nil
	}

	parent, err := a.getGroup(ctx, tenantID, input.ParentID)
	if err != nil {
		return err
	}
	// A group and its parent must be anchored to the same customer when
	// both are anchored.
	if input.CustomerID != "" && parent.CustomerID != "" &&
		parent.CustomerID != input.CustomerID {
		return invalid("parent group belongs to a different customer")
	}
	if selfID != "" {
		if err := a.ensureNoCycle(ctx, tenantID, selfID, input.ParentID); err != nil {
			return err
		}
	}
	return nil
}

// getGroup loads one tenant-owned group.
func (a *Admin) getGroup(ctx context.Context, tenantID, groupID string) (GroupDTO, error) {
	var group GroupDTO
	err := a.pool.QueryRow(ctx,
		`SELECT id::text, COALESCE(customer_id::text, ''),
		        COALESCE(parent_id::text, ''), name
		 FROM device_group
		 WHERE id = $1 AND tenant_id = $2`,
		groupID, tenantID).Scan(&group.ID, &group.CustomerID,
		&group.ParentID, &group.Name)
	if err != nil {
		return GroupDTO{}, mapSQLError(err)
	}
	return group, nil
}

// ensureNoCycle rejects the parent choice when walking parents from
// parentID reaches selfID, which would make the subtree cyclic.
func (a *Admin) ensureNoCycle(ctx context.Context, tenantID, selfID, parentID string) error {
	current := parentID
	seen := map[string]bool{}
	for current != "" {
		if current == selfID {
			return invalid("cannot move a group into one of its descendants")
		}
		if seen[current] {
			// A pre-existing cycle is a data integrity fault; stop.
			return fmt.Errorf("existing group cycle detected at %s", current)
		}
		seen[current] = true
		group, err := a.getGroup(ctx, tenantID, current)
		if err != nil {
			return err
		}
		current = group.ParentID
	}
	return nil
}

// DeviceCountsByCustomer returns non-decommissioned device counts keyed
// by customer id in one query. Unassigned devices are excluded.
func (a *Admin) DeviceCountsByCustomer(ctx context.Context, tenantID string) (map[string]int64, error) {
	return a.deviceCounts(ctx, "customer_id", tenantID)
}

// DeviceCountsByGroup returns non-decommissioned device counts keyed by
// group id in one query. Ungrouped devices are excluded.
func (a *Admin) DeviceCountsByGroup(ctx context.Context, tenantID string) (map[string]int64, error) {
	return a.deviceCounts(ctx, "group_id", tenantID)
}

// deviceCounts groups non-decommissioned device rows by column. column
// is always one of two fixed Go literals, never external input, so
// splicing it into the query is safe.
func (a *Admin) deviceCounts(ctx context.Context, column, tenantID string) (map[string]int64, error) {
	// The grouped column IS NOT NULL filter excludes unassigned devices: a
	// NULL customer/group would otherwise form a GROUP BY group whose NULL
	// cannot be scanned into the string map key and 500 the whole request.
	rows, err := a.pool.Query(ctx, fmt.Sprintf(
		`SELECT %s::text, count(*)
		 FROM device
		 WHERE tenant_id = $1 AND state <> 'DECOMMISSIONED' AND %s IS NOT NULL
		 GROUP BY 1`, column, column), tenantID)
	if err != nil {
		return nil, fmt.Errorf("load device counts: %w", err)
	}
	defer rows.Close()

	counts := map[string]int64{}
	for rows.Next() {
		var id string
		var count int64
		if err := rows.Scan(&id, &count); err != nil {
			return nil, fmt.Errorf("scan device count: %w", err)
		}
		counts[id] = count
	}
	return counts, rows.Err()
}
