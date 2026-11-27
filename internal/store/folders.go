package store

import (
	"context"
	"fmt"
)

// Folder groups job sources. Names are unique ignoring case.
type Folder struct {
	ID   int64
	Name string
}

// CreateFolder returns ErrDuplicate when a folder with the same name (ignoring
// case) exists.
func CreateFolder(ctx context.Context, db DBTX, name string) (Folder, error) {
	var f Folder
	err := db.QueryRow(ctx, "INSERT INTO folders (name) VALUES ($1) RETURNING id, name", name).Scan(&f.ID, &f.Name)
	if err != nil {
		return Folder{}, fmt.Errorf("create folder: %w", mapError(err))
	}
	return f, nil
}

// ListFolders returns folders ordered by name.
func ListFolders(ctx context.Context, db DBTX) ([]Folder, error) {
	rows, err := db.Query(ctx, "SELECT id, name FROM folders ORDER BY lower(name), id")
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Name); err != nil {
			return nil, fmt.Errorf("scan folder: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
