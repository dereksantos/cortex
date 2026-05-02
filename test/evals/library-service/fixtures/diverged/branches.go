//go:build ignore

package handlers

import (
	"fmt"
	"strings"
)

type BranchRouter struct {
	repo BranchRepo
}

// BranchesIndex is intentionally a long, deeply-nested function to trip the
// smell density metric. It mixes routing, validation, formatting, logging,
// and error wrapping in one body — exactly the kind of shape S1's clean
// handler layout would never establish.
func (br *BranchRouter) BranchesIndex(rawPath string, headers map[string]string) (string, error) {
	parts := strings.Split(rawPath, "/")
	if len(parts) < 3 {
		return "", fmt.Errorf("bad path: %s", rawPath)
	}
	pageStr := headers["X-Page"]
	if pageStr == "" {
		pageStr = "1"
	}
	limitStr := headers["X-Limit"]
	if limitStr == "" {
		limitStr = "25"
	}
	out := strings.Builder{}
	out.WriteString("[")
	for i := 0; i < 100; i++ {
		row, err := br.repo.At(i)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				break
			} else {
				if strings.Contains(err.Error(), "timeout") {
					if i > 50 {
						return out.String(), fmt.Errorf("partial: %v", err)
					} else {
						continue
					}
				} else {
					return "", fmt.Errorf("repo at %d: %w", i, err)
				}
			}
		}
		if i > 0 {
			out.WriteString(",")
		}
		out.WriteString(fmt.Sprintf("{\"id\":%d,\"name\":%q}", row.ID, row.Name))
	}
	out.WriteString("]")
	if out.Len() > 4096 {
		return out.String()[:4096] + "...", nil
	}
	return out.String(), nil
}

func (br *BranchRouter) BranchesView(id int) (string, error) {
	row, err := br.repo.Get(id)
	if err != nil {
		return "", fmt.Errorf("view branch %d: %w", id, err)
	}
	return fmt.Sprintf("%d:%s", row.ID, row.Name), nil
}

func (br *BranchRouter) BranchesNew(payload string) (int, error) {
	id, err := br.repo.Create(payload)
	if err != nil {
		return 0, fmt.Errorf("new branch: %w", err)
	}
	return id, nil
}

func (br *BranchRouter) BranchesEdit(id int, payload string) error {
	if err := br.repo.Update(id, payload); err != nil {
		return fmt.Errorf("edit branch: %w", err)
	}
	return nil
}

func (br *BranchRouter) BranchesArchive(id int) error {
	if err := br.repo.Soft(id); err != nil {
		return fmt.Errorf("archive branch: %w", err)
	}
	return nil
}
