package service

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// ZeroFileUsage sets back to zero what each file of the customers matched by
// clientWhere has carried.
//
// A file's counters are its share of its customer's usage -- the per-user,
// per-tunnel table on the subscription page sums them against the usage
// above it -- so every reset of a customer's usage resets them with it.
// Left alone they kept the old period, and a customer renewed this month was
// shown a table larger than everything they had used.
func ZeroFileUsage(db *gorm.DB, clientWhere string, args ...any) error {
	q := db.Model(&model.Account{})
	if clientWhere != "" {
		q = q.Where("client_id IN (SELECT id FROM clients WHERE "+clientWhere+")", args...)
	} else {
		q = q.Where("1 = 1")
	}
	// The caller's condition is a string, and the automatic narrowing is a
	// clause on statements against the clients table, so it never reaches
	// inside the subquery: a reset by a reseller -- of a selection, of a
	// group, of everything -- zeroed the files of any customer the
	// condition happened to name, and of every customer on the panel when
	// it named none. Narrow by hand to the caller's own customers, the set
	// the customers' totals beside it were narrowed to.
	if db.Statement != nil && db.Statement.Context != nil {
		if sc := ScopeOf(db.Statement.Context); sc.Restricted {
			q = q.Where("client_id IN (SELECT id FROM clients WHERE owner_id IN ?)", sc.OwnerList())
		}
	}
	if err := q.UpdateColumns(map[string]any{"up_bytes": 0, "down_bytes": 0}).Error; err != nil {
		return fmt.Errorf("service: reset the files' usage: %w", err)
	}
	return nil
}
