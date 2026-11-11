// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// parseExpression splits "a op b = ?" and recomputes the expected answer,
// returning operands, operator symbol and the expected result.
func parseExpression(t *testing.T, expr string) (int, string, int, int) {
	t.Helper()
	expr = strings.TrimSuffix(expr, " = ?")
	fields := strings.Fields(expr)
	if len(fields) != 3 {
		t.Fatalf("malformed expression %q", expr)
	}
	a, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("bad left operand: %v", err)
	}
	b, err := strconv.Atoi(fields[2])
	if err != nil {
		t.Fatalf("bad right operand: %v", err)
	}
	var want int
	switch fields[1] {
	case "+":
		want = a + b
	case "-":
		want = a - b
	case "×":
		want = a * b
	case "÷":
		want = a / b
	default:
		t.Fatalf("unknown operator %q", fields[1])
	}
	return a, fields[1], b, want
}

// TestGenerateArithmeticBounds runs many generations and asserts the
// arithmetic rules: operands 1..10, non-negative subtraction, even division
// with no zero divisor, and a correct answer.
func TestGenerateArithmeticBounds(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 4000; i++ {
		expr, answer := generate()
		seen[expr] = true
		a, op, b, want := parseExpression(t, expr)
		if answer != want {
			t.Fatalf("%s = %d, want %d", expr, answer, want)
		}
		if a < 1 || a > captchaOperandMax || b < 1 || b > captchaOperandMax {
			t.Fatalf("operand out of range in %q", expr)
		}
		if answer < 0 {
			t.Fatalf("negative answer for %q", expr)
		}
		switch op {
		case "-":
			if a < b {
				t.Fatalf("subtraction goes negative: %q", expr)
			}
		case "÷":
			if b == 0 {
				t.Fatalf("division by zero: %q", expr)
			}
			if a%b != 0 {
				t.Fatalf("division does not divide evenly: %q", expr)
			}
			if a > captchaOperandMax {
				t.Fatalf("dividend exceeds bound: %q", expr)
			}
		}
	}
	// Four operators should all appear across a large sample.
	for _, op := range []string{"+", "-", "×", "÷"} {
		found := false
		for expr := range seen {
			if strings.Contains(expr, " "+op+" ") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("operator %s never generated", op)
		}
	}
}

// TestCaptchaIssueAndVerify covers the happy path and single-use semantics.
func TestCaptchaIssueAndVerify(t *testing.T) {
	s := newCaptchaStore()
	id, expr := s.issue()
	if id == "" || expr == "" {
		t.Fatal("issue returned empty challenge")
	}
	_, _, _, want := parseExpression(t, expr)

	if !s.verify(id, want) {
		t.Fatal("correct answer rejected")
	}
	// The challenge is consumed; the same answer must not pass again.
	if s.verify(id, want) {
		t.Fatal("consumed challenge accepted again")
	}
	// A wrong answer consumes the challenge too.
	id2, expr2 := s.issue()
	_, _, _, want2 := parseExpression(t, expr2)
	if s.verify(id2, want2+1) {
		t.Fatal("wrong answer accepted")
	}
	if s.verify(id2, want2) {
		t.Fatal("challenge reused after wrong answer")
	}
	// Unknown and empty ids always fail.
	if s.verify("does-not-exist", 1) {
		t.Fatal("unknown id accepted")
	}
	if s.verify("", 1) {
		t.Fatal("empty id accepted")
	}
}

// TestCaptchaExpiry ensures an expired challenge is rejected on use.
func TestCaptchaExpiry(t *testing.T) {
	base := time.Now()
	s := newCaptchaStore()
	s.now = func() time.Time { return base }

	id, expr := s.issue()
	_, _, _, want := parseExpression(t, expr)

	s.now = func() time.Time { return base.Add(captchaTTL + time.Second) }
	if s.verify(id, want) {
		t.Fatal("expired challenge accepted")
	}
}

// TestCaptchaEviction ensures expired entries are purged and the store never
// exceeds its bound.
func TestCaptchaEviction(t *testing.T) {
	base := time.Now()
	s := newCaptchaStore()
	s.now = func() time.Time { return base }

	for i := 0; i < captchaMax+50; i++ {
		s.issue()
	}
	s.mu.Lock()
	got := len(s.entries)
	s.mu.Unlock()
	if got > captchaMax {
		t.Fatalf("store grew to %d, bound %d", got, captchaMax)
	}

	// Advancing the clock past the TTL makes a subsequent issue purge all
	// previously inserted entries.
	s.now = func() time.Time { return base.Add(captchaTTL + time.Second) }
	s.issue()
	s.mu.Lock()
	got = len(s.entries)
	s.mu.Unlock()
	if got != 1 {
		t.Fatalf("expected only the fresh entry, got %d", got)
	}
}
