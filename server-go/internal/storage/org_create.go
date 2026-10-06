package storage

import "errors"

// FreeOrgsPerUser is how many free organizations one account may create
// (EXC-553). Its personal organization is the one.
const FreeOrgsPerUser = 1

// ErrFreeOrgLimitReached refuses a free organization to an account that
// already created its allowance of them.
var ErrFreeOrgLimitReached = errors.New("free organization limit reached")

// ErrOrgSlugTaken refuses an organization whose slug another one uses.
var ErrOrgSlugTaken = errors.New("organization slug is already taken")
