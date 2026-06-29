package observerstore

import "errors"

// CategorizedError wraps an underlying error with a FailureCategory tag so
// driver / executor failure-return sites can attach a stable analytics
// bucket without changing control flow. It implements Unwrap so existing
// errors.Is / errors.As callers see through it unchanged.
//
// Use Categorize() at the return site, and CategoryOf() (which walks the
// Unwrap chain) on the receiving side. Re-wrapping with another category is
// allowed; the outermost category wins, mirroring how callers think about
// the layer they own.
type CategorizedError struct {
	Category FailureCategory
	Err      error
}

func (e *CategorizedError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *CategorizedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Categorize tags err with cat. Returns nil if err is nil so callers can
// write `return Categorize(doThing(), FailMissingFile)` without an inline
// nil check, matching `fmt.Errorf` ergonomics.
func Categorize(err error, cat FailureCategory) error {
	if err == nil {
		return nil
	}
	return &CategorizedError{Category: cat, Err: err}
}

// Categorized is the interface CategoryOf inspects for tags. Error types
// outside this package (e.g. driver.MCPToolError) implement it so they don't
// need to wrap themselves in CategorizedError just to carry a tag.
type Categorized interface {
	FailureCategory() FailureCategory
}

// FailureCategory satisfies the Categorized interface for CategorizedError.
func (e *CategorizedError) FailureCategory() FailureCategory {
	if e == nil {
		return FailUnknown
	}
	return e.Category
}

// CategoryOf returns the category tag on err (or any error in its Unwrap
// chain). It accepts both *CategorizedError and any error implementing the
// Categorized interface. Returns FailUnknown when err is nil or no tag is
// found — the sentinel makes "no tag" indistinguishable from "tag explicitly
// unknown" at the analytics layer, which is fine because both bucket to
// "unclassified".
func CategoryOf(err error) FailureCategory {
	if err == nil {
		return FailUnknown
	}
	var c Categorized
	if errors.As(err, &c) && c != nil {
		if cat := c.FailureCategory(); cat != "" {
			return cat
		}
	}
	return FailUnknown
}
