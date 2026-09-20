package service

import "errors"

// ErrYixiezuoAlreadyImported returns the existing issue without reapplying material.
var ErrYixiezuoAlreadyImported = errors.New("this 易协作 issue is already imported")
