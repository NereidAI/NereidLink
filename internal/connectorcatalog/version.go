// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// version.go compares connector plugin version strings for NL's unified
// version selection (ADR-0026). Edge-advertised type_version values are not
// guaranteed to be strict semver (examples in the field are "2.1", "1.0",
// "1.2"), so comparison is deliberately tolerant rather than a full semver
// implementation.

package connectorcatalog

import "strconv"

// CompareVersions orders two connector type_version strings. It returns -1,
// 0 or 1 when a is respectively lower than, equal to or higher than b.
//
// The strings are split on '.' and compared field by field: fields that parse
// as integers are compared numerically (so "1.10" > "1.9"), non-numeric fields
// are compared lexicographically, and a leading optional 'v'/'V' is ignored.
// A prefix that compares equal ranks the longer string higher ("1.2.1" >
// "1.2"). Completely unparseable/empty inputs fall back to plain lexicographic
// ordering so selection stays deterministic even for exotic version labels.
func CompareVersions(a, b string) int {
	pa, pb := fields(a), fields(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if c := compareField(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(pa) < len(pb):
		return -1
	case len(pa) > len(pb):
		return 1
	default:
		return 0
	}
}

// fields splits a version string into comparable dot-separated fields after
// stripping a single leading 'v'/'V'. Empty input yields a single empty field
// so two empty versions compare equal rather than panicking.
func fields(v string) []string {
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		v = v[1:]
	}
	if v == "" {
		return []string{""}
	}
	out := []string{}
	start := 0
	for i := 0; i < len(v); i++ {
		if v[i] == '.' {
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	out = append(out, v[start:])
	return out
}

// compareField compares one dot-separated version field numerically when both
// sides are integers, otherwise lexicographically.
func compareField(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	if errA == nil && errB == nil {
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		default:
			return 0
		}
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
