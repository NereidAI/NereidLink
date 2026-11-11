// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package modelinventory

import "errors"

// ErrInvalidModel reports a manifest model entry missing its key or carrying
// an unrecognized lifecycle state.
var ErrInvalidModel = errors.New("model inventory entry invalid")

// ErrGatewayNotFound reports the target is not a gateway in the tenant.
var ErrGatewayNotFound = errors.New("gateway not found")

// ErrNotFound reports a system seed record was expected but missing.
var ErrNotFound = errors.New("model inventory record not found")
