package storage

import "errors"

// ErrInviteInvalid is the one answer for an invite token that is unknown,
// already used or expired, so a caller learns nothing about which.
var ErrInviteInvalid = errors.New("invite link is invalid or has expired")

// ErrAlreadyOrgMember refuses an invite for someone already in the org; the
// invite stays unspent.
var ErrAlreadyOrgMember = errors.New("already a member of this organization")
