package ai

import (
	"errors"

	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

// ApplyChangeset writes the proposals the user accepted (POST /api/ai/apply)
// and returns the paths it touched, for the git commit: each created,
// updated or deleted path, and for a move the old path, the new path and
// every note whose links were rewritten.
//
// First every update, move and delete that carries a BaseETag is checked
// against the vault; if any note is gone or changed since it was proposed,
// nothing is written and the error is a 400 vault error with Python's text
// ("<path> 已不存在，请重新生成" / "<path> 在生成后被修改过，请重新生成"). Then the
// operations run in order: creates overwrite (Python semantics), updates
// pass BaseETag to vault.WriteNote, moves rewrite links, unknown operations
// are ignored.
//
// When an operation fails midway, the error is returned together with the
// paths touched before it (Python returned nothing; the HTTP layer can still
// commit what was written). Errors are vault errors (map them with
// vault.StatusOf) or I/O errors.
func ApplyChangeset(v *vault.Vault, ops []Proposal) ([]string, error) {
	for _, op := range ops {
		if op.Op != OpUpdate && op.Op != OpDelete && op.Op != OpMove {
			continue
		}
		if op.BaseETag == nil || *op.BaseETag == "" {
			continue
		}
		_, data, err := v.ReadBytes(op.Path)
		if errors.Is(err, vault.ErrNotFound) {
			return nil, vault.Invalid(op.Path + " 已不存在，请重新生成")
		}
		if err != nil {
			return nil, err
		}
		if vault.ETag(data) != *op.BaseETag {
			return nil, vault.Invalid(op.Path + " 在生成后被修改过，请重新生成")
		}
	}

	touched := []string{}
	for _, op := range ops {
		switch op.Op {
		case OpCreate, OpUpdate:
			var base *string
			if op.Op == OpUpdate {
				base = op.BaseETag
			}
			if _, err := v.WriteNote(op.Path, op.After, base); err != nil {
				return touched, err
			}
			touched = append(touched, op.Path)
		case OpMove:
			result, err := v.Move(op.Path, op.To, true)
			if err != nil {
				return touched, err
			}
			touched = append(touched, op.Path, result.Path)
			touched = append(touched, result.UpdatedLinksIn...)
		case OpDelete:
			if err := v.Delete(op.Path); err != nil {
				return touched, err
			}
			touched = append(touched, op.Path)
		}
	}
	return touched, nil
}

// WrittenPaths lists where the accepted operations leave content, for
// marking the source chats as distilled into them: the destination of each
// move and the path of every other operation except deletes.
func WrittenPaths(ops []Proposal) []string {
	written := []string{}
	for _, op := range ops {
		switch op.Op {
		case OpDelete:
		case OpMove:
			written = append(written, op.To)
		default:
			written = append(written, op.Path)
		}
	}
	return written
}

// CommitMessage is the git commit message of an applied changeset:
// "ai: <summary>" ("ai: organize notes" without one), cut to 120 characters.
func CommitMessage(summary string) string {
	if summary == "" {
		summary = "organize notes"
	}
	return pystr.Head("ai: "+summary, 120)
}
