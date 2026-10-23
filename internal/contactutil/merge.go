package contactutil

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// MergeReport is what MergeDuplicateContacts did (or, in a dry run, would do).
type MergeReport struct {
	Groups     int      // sets of contacts that are one subscriber
	Duplicates int      // rows folded into an older one
	Moved      int64    // child rows re-pointed (messages, notes, occurrences...)
	Skipped    []string // groups left untouched, with the reason
}

// MergeDuplicateContacts folds contacts that PhoneIdentities says are the same
// subscriber into the oldest one (the row an agent created and owns, see
// contactByPhone): every table with a *contact_id column is re-pointed at the
// survivor, and the duplicates are soft-deleted, not removed, so a bad merge can
// be undone. Each group runs in its own transaction; a group that hits a
// constraint is rolled back whole and reported. With apply=false nothing is
// written and the report only counts what would happen.
func MergeDuplicateContacts(db *gorm.DB, orgID uuid.UUID, apply bool) (MergeReport, error) {
	var rep MergeReport
	var all []models.Contact
	if err := db.Where("organization_id = ?", orgID).Order("created_at ASC").Find(&all).Error; err != nil {
		return rep, err
	}

	// Union-find over contacts that share any phone identity.
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	for i, c := range all {
		for _, id := range PhoneIdentities(c.PhoneNumber) {
			if j, ok := owner[id]; ok {
				if ri, rj := find(i), find(j); ri != rj {
					// Keep the older (smaller index, list is ordered) as root.
					if ri < rj {
						parent[rj] = ri
					} else {
						parent[ri] = rj
					}
				}
			} else {
				owner[id] = i
			}
		}
	}
	groups := map[int][]int{}
	for i := range all {
		r := find(i)
		groups[r] = append(groups[r], i)
	}
	roots := make([]int, 0, len(groups))
	for r, m := range groups {
		if len(m) > 1 {
			roots = append(roots, r)
		}
	}
	sort.Ints(roots)

	type col struct{ Table, Column string }
	var cols []col
	if err := db.Raw(`SELECT table_name AS "table", column_name AS "column"
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type = 'uuid'
		  AND column_name LIKE '%contact_id' AND table_name <> 'contacts'`).Scan(&cols).Error; err != nil {
		return rep, err
	}

	pairs := make([][2]string, len(cols))
	for i, c := range cols {
		pairs[i] = [2]string{c.Table, c.Column}
	}

	for _, r := range roots {
		members := groups[r]
		keep := all[members[0]]
		var dups []uuid.UUID
		for _, i := range members[1:] {
			dups = append(dups, all[i].ID)
		}
		rep.Groups++
		rep.Duplicates += len(dups)
		if !apply {
			continue
		}
		moved, err := mergeGroup(db, keep, all, members[1:], dups, pairs)
		if err != nil {
			rep.Duplicates -= len(dups)
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s (%s): %v", keep.PhoneNumber, keep.ID, err))
			continue
		}
		rep.Moved += moved
	}
	return rep, nil
}

func mergeGroup(db *gorm.DB, keep models.Contact, all []models.Contact, dupIdx []int, dups []uuid.UUID, cols [][2]string) (int64, error) {
	var moved int64
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, c := range cols {
			res := tx.Exec(fmt.Sprintf(`UPDATE %q SET %q = ? WHERE %q IN ?`, c[0], c[1], c[1]), keep.ID, dups)
			if res.Error != nil {
				return fmt.Errorf("%s.%s: %w", c[0], c[1], res.Error)
			}
			moved += res.RowsAffected
		}
		// idx_contacts_org_phone is unique over soft-deleted rows too, so retire the
		// duplicates' numbers (tagged, not lost) before the survivor can take the
		// canonical one. It also stops a later inbound from resurrecting a dup.
		if err := tx.Exec(`UPDATE contacts SET phone_number = phone_number || '#merged-' || left(id::text, 8) WHERE id IN ?`, dups).Error; err != nil {
			return err
		}
		updates := map[string]any{}
		if p := CanonicalPhone(keep.PhoneNumber); p != keep.PhoneNumber {
			updates["phone_number"] = p
		}
		for _, i := range dupIdx {
			d := all[i]
			if keep.ProfileName == "" && d.ProfileName != "" {
				updates["profile_name"] = d.ProfileName
				keep.ProfileName = d.ProfileName
			}
			if keep.AssignedUserID == nil && d.AssignedUserID != nil {
				updates["assigned_user_id"] = *d.AssignedUserID
				keep.AssignedUserID = d.AssignedUserID
			}
		}
		if len(updates) > 0 {
			if err := tx.Model(&models.Contact{}).Where("id = ?", keep.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		return tx.Where("id IN ?", dups).Delete(&models.Contact{}).Error
	})
	return moved, err
}
