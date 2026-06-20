package merge

import (
	"bytes"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// DiskAction describes what the engine should do with the underlying file.
type DiskAction int

const (
	ActionApply         DiskAction = iota // write catchup's content
	ActionAutoMerge                       // disjoint hunks → merged content
	ActionWriteConflict                   // overlap → on-disk conflict marker
	ActionWriteSidecar                    // edit_vs_delete / binary / case_collision
	ActionApplyRename                     // catchup rename → mv on disk
	ActionApplyDelete                     // catchup delete → rm on disk
	ActionNoop                            // no disk write
)

// LogKind identifies the conflict category logged to conflicts.jsonl.
// "resolved" is emitted by the resolver, not here.
type LogKind string

const (
	KindOverlap         LogKind = "overlap"
	KindDeleteVsEdit    LogKind = "delete_vs_edit"
	KindEditVsDelete    LogKind = "edit_vs_delete"
	KindRenameCollision LogKind = "rename_collision"
	KindBinary          LogKind = "binary"
	KindCaseCollision   LogKind = "case_collision"
	KindNone            LogKind = "" // no log entry
)

type LogFormat string

const (
	FormatCallout LogFormat = "callout"
	FormatComment LogFormat = "comment"
	FormatMarker  LogFormat = "marker"
	FormatSidecar LogFormat = "sidecar"
	FormatNone    LogFormat = ""
)

// Context carries everything Classify needs that isn't in the two ops.
// Base is the base-version content of the path (for three-way merge);
// empty string means "no base" (true create). DiffMode is "leyline" or
// "git". Server/ClientKeyname populate marker headers. TS is the ISO
// string for marker/sidecar names.
type Context struct {
	Base          string
	DiffMode      string // "leyline" or "git"
	ServerKeyname string
	ClientKeyname string
	TS            string
}

// Decision is the engine's classification output: what to do with the
// catchup op, what to put on disk, what to log, what the rebased staged
// op should be (nil means drop the staged op).
type Decision struct {
	DiskAction        DiskAction
	DiskContent       []byte       // for ActionApply / ActionAutoMerge / ActionWriteConflict
	SidecarPath       string       // for ActionWriteSidecar
	SidecarContent    []byte       // for ActionWriteSidecar
	LogKind           LogKind
	LogFormat         LogFormat
	ReplacementStaged *protocol.Op // post-rebase staged op (nil to drop)
}

// Classify determines the disk action and rebase outcome for one
// (catchup, staged) pair on the same path. staged may be nil (no current
// local op). The catchup op's Path/From/To identifies which file; the
// caller selects the staged op by matching path.
func Classify(catchup protocol.Op, staged *protocol.Op, ctx Context) Decision {
	if staged == nil {
		switch catchup.Type {
		case protocol.OpWrite:
			return Decision{DiskAction: ActionApply, DiskContent: catchup.Data}
		case protocol.OpDelete:
			return Decision{DiskAction: ActionApplyDelete}
		case protocol.OpRename:
			return Decision{DiskAction: ActionApplyRename}
		}
		return Decision{DiskAction: ActionNoop}
	}
	// Both ops touch the same path; route on (catchup.Type, staged.Type).
	switch catchup.Type {
	case protocol.OpWrite:
		switch staged.Type {
		case protocol.OpWrite:
			return classifyWriteWrite(catchup, *staged, ctx)
		case protocol.OpDelete:
			// edit_vs_delete from server's view: the server wrote, the client
			// deleted. Keep the staged delete; sidecar the incoming content so
			// the client can decide whether to restore it.
			// PreHash re-anchors against the server's current state
			// (= catchup.Data) — same contract as classifyWriteWrite's
			// overlap branches; keeping the old pre_hash would re-fire
			// stale_base on every push of the surviving delete.
			newPre := protocol.HashBytes(catchup.Data)
			return Decision{
				DiskAction:     ActionWriteSidecar,
				SidecarPath:    SidecarPath(catchup.Path, ctx.TS),
				SidecarContent: catchup.Data,
				LogKind:        KindEditVsDelete,
				LogFormat:      FormatSidecar,
				ReplacementStaged: &protocol.Op{
					Seq:     staged.Seq,
					Type:    protocol.OpDelete,
					Path:    staged.Path,
					PreHash: &newPre,
					TS:      staged.TS,
				},
			}
		case protocol.OpRename:
			// Catchup write(Y) vs. staged rename(X→Y): destination collision.
			// Catchup write wins; staged rename is rewritten as a sidecar write
			// with pre_hash:null. X is tombstoned via the dropped rename (no
			// rename → X stays as-is locally; the manifest reconciles on next
			// sync).
			return Decision{
				DiskAction:     ActionApply,
				DiskContent:    catchup.Data,
				SidecarPath:    SidecarPath(catchup.Path, ctx.TS),
				SidecarContent: staged.Data,
				LogKind:        KindRenameCollision,
				LogFormat:      FormatSidecar,
				ReplacementStaged: &protocol.Op{
					Seq:  staged.Seq,
					Type: protocol.OpWrite,
					Path: SidecarPath(catchup.Path, ctx.TS),
					Data: staged.Data,
					TS:   staged.TS,
					// PreHash: nil — true create at the sidecar path
				},
			}
		}
	case protocol.OpDelete:
		switch staged.Type {
		case protocol.OpWrite:
			// delete_vs_edit: catchup deleted the path, client has a pending
			// write. Keep the staged content with a conflict header so the
			// client can resolve.
			format := chooseFormatForPath(catchup.Path, ctx.DiffMode)
			// Binary guard: same condition as classifyWriteWrite. Binary content
			// can't hold text markers or comment blocks; sidecar it instead.
			if staged.Binary || !isText(staged.Data) || format == FormatSidecar {
				return Decision{
					DiskAction:     ActionWriteSidecar,
					SidecarPath:    SidecarPath(catchup.Path, ctx.TS),
					SidecarContent: staged.Data,
					LogKind:        KindDeleteVsEdit,
					LogFormat:      FormatSidecar,
					ReplacementStaged: &protocol.Op{
						Seq:  staged.Seq,
						Type: protocol.OpWrite,
						Path: SidecarPath(catchup.Path, ctx.TS),
						Data: staged.Data,
						TS:   staged.TS,
					},
				}
			}
			content := deleteVsEditContent(staged.Data, catchup.Path, ctx)
			return Decision{
				DiskAction:  ActionWriteConflict,
				DiskContent: content,
				LogKind:     KindDeleteVsEdit,
				LogFormat:   format,
				ReplacementStaged: &protocol.Op{
					Seq:  staged.Seq,
					Type: protocol.OpWrite,
					Path: catchup.Path,
					Data: content,
					TS:   staged.TS,
				},
			}
		case protocol.OpDelete:
			// Both sides deleted the same path. Drop the staged delete; the
			// catchup delete already advances the manifest tombstone.
			return Decision{
				DiskAction:        ActionNoop,
				LogKind:           KindNone,
				ReplacementStaged: nil, // explicit: drop staged
			}
		case protocol.OpRename:
			// Catchup delete(X) vs. staged rename(X→Y): X is gone so the
			// rename source is stale. Drop the staged rename and replace with
			// write(Y, pre_hash:null), stashing staged.Data (the bytes the
			// rename was carrying) under the new path as best-effort
			// preservation.
			return Decision{
				DiskAction: ActionNoop,
				LogKind:    KindRenameCollision,
				LogFormat:  FormatNone, // surfaced via log without on-disk marker
				ReplacementStaged: &protocol.Op{
					Seq:  staged.Seq,
					Type: protocol.OpWrite,
					Path: staged.To,
					Data: staged.Data,
					TS:   staged.TS,
					// PreHash: nil — true create
				},
			}
		}
	case protocol.OpRename:
		// For rename catchup, the staged op's relation to the catchup is
		// determined by whether the staged op is at catchup.From (source)
		// or catchup.To (destination). The two have different rows.
		stagedPath := staged.Path
		if staged.Type == protocol.OpRename {
			stagedPath = staged.From
		}
		atSource := stagedPath == catchup.From
		atDest := stagedPath == catchup.To || (staged.Type == protocol.OpRename && staged.To == catchup.To)

		switch staged.Type {
		case protocol.OpWrite:
			if atDest && !atSource {
				// Catchup rename(X→Y) vs. staged write(Y): destination collision.
				// Rename applies; staged content goes to sidecar with
				// pre_hash:null.
				return Decision{
					DiskAction:     ActionApplyRename,
					SidecarPath:    SidecarPath(catchup.To, ctx.TS),
					SidecarContent: staged.Data,
					LogKind:        KindRenameCollision,
					LogFormat:      FormatSidecar,
					ReplacementStaged: &protocol.Op{
						Seq:  staged.Seq,
						Type: protocol.OpWrite,
						Path: SidecarPath(catchup.To, ctx.TS),
						Data: staged.Data,
						TS:   staged.TS,
						// PreHash: nil
					},
				}
			}
			// Catchup rename(X→Y) vs. staged write(X): rebase the staged
			// write to write(Y).
			return Decision{
				DiskAction: ActionApplyRename,
				ReplacementStaged: &protocol.Op{
					Seq:  staged.Seq,
					Type: protocol.OpWrite,
					Path: catchup.To,
					Data: staged.Data,
					TS:   staged.TS,
				},
			}
		case protocol.OpRename:
			// Both sides rename the same source: catchup wins. Log
			// rename_collision only when the destinations differ.
			if catchup.From == staged.From {
				logKind := KindNone
				if catchup.To != staged.To {
					logKind = KindRenameCollision
				}
				return Decision{
					DiskAction:        ActionApplyRename,
					LogKind:           logKind,
					ReplacementStaged: nil, // drop staged; catchup wins
				}
			}
			// Different sources — defer to defensive fallthrough.
		case protocol.OpDelete:
			// Catchup rename(X→Y) vs. staged delete(X): rebase delete to
			// delete(Y).
			return Decision{
				DiskAction: ActionApplyRename,
				ReplacementStaged: &protocol.Op{
					Seq:     staged.Seq,
					Type:    protocol.OpDelete,
					Path:    catchup.To,
					PreHash: staged.PreHash,
					TS:      staged.TS,
				},
			}
		}
	}
	// Unhandled combo — defensive.
	return Decision{DiskAction: ActionNoop, LogKind: KindNone}
}

// classifyWriteWrite handles the (OpWrite catchup, OpWrite staged) case:
// tries three-way merge first; falls back to the per-format conflict
// serializer on overlap; takes the sidecar path for binary content.
func classifyWriteWrite(catchup, staged protocol.Op, ctx Context) Decision {
	// Binary path: always conflict, last-writer-wins on disk + sidecar.
	if catchup.Binary || staged.Binary || !isText(catchup.Data) || !isText(staged.Data) {
		return Decision{
			DiskAction:     ActionApply,
			DiskContent:    catchup.Data,
			SidecarPath:    SidecarPath(catchup.Path, ctx.TS),
			SidecarContent: staged.Data,
			LogKind:        KindBinary,
			LogFormat:      FormatSidecar,
			ReplacementStaged: &protocol.Op{
				Seq:  staged.Seq,
				Type: protocol.OpWrite,
				Path: SidecarPath(catchup.Path, ctx.TS),
				Data: staged.Data,
				TS:   staged.TS,
			},
		}
	}
	res := ThreeWayMerge(ctx.Base, string(catchup.Data), string(staged.Data))
	if !res.HasConflict {
		merged := []byte(res.Content)
		// Auto-merge: replace staged data + advance pre_hash to catchup's hash.
		newPre := protocol.HashBytes(catchup.Data)
		return Decision{
			DiskAction:  ActionAutoMerge,
			DiskContent: merged,
			LogKind:     KindNone,
			ReplacementStaged: &protocol.Op{
				Seq:     staged.Seq,
				Type:    protocol.OpWrite,
				Path:    staged.Path,
				Data:    merged,
				PreHash: &newPre,
				TS:      staged.TS,
			},
		}
	}
	// Overlap: select on-disk format by extension + diff_mode.
	format := chooseFormatForPath(catchup.Path, ctx.DiffMode)
	var content []byte
	switch format {
	case FormatCallout:
		s, _ := WriteCalloutFile(ctx.Base, string(catchup.Data), string(staged.Data), ctx.ServerKeyname, ctx.ClientKeyname, ctx.TS)
		content = []byte(s)
	case FormatComment:
		// canonical content = server; comment block appended below.
		style, _ := CommentStyleForExt(extOf(catchup.Path))
		block := WriteCommentBlock(style, ctx.ServerKeyname, ctx.ClientKeyname, ctx.TS, string(staged.Data))
		content = append(append([]byte(nil), catchup.Data...), '\n')
		content = append(content, block...)
	case FormatMarker:
		content = []byte(FormatGitMarkers(ctx.ServerKeyname, ctx.TS, string(catchup.Data), string(staged.Data)))
	default: // FormatSidecar
		return Decision{
			DiskAction:     ActionApply,
			DiskContent:    catchup.Data,
			SidecarPath:    SidecarPath(catchup.Path, ctx.TS),
			SidecarContent: staged.Data,
			LogKind:        KindOverlap,
			LogFormat:      FormatSidecar,
			ReplacementStaged: &protocol.Op{
				Seq:  staged.Seq,
				Type: protocol.OpWrite,
				Path: SidecarPath(catchup.Path, ctx.TS),
				Data: staged.Data,
				TS:   staged.TS,
			},
		}
	}
	// PreHash anchors the replacement op against the SERVER's current state
	// (= catchup.Data), not the locally-written callout. Hashing the callout
	// would force the next push to re-fire stale_base because the server
	// has catchup.Data at HEAD, not the merged conflict content. Symmetric
	// with the auto-merge branch above and verified by
	// TestClassifyConflictCallout_PreHashContract.
	newPre := protocol.HashBytes(catchup.Data)
	return Decision{
		DiskAction:  ActionWriteConflict,
		DiskContent: content,
		LogKind:     KindOverlap,
		LogFormat:   format,
		ReplacementStaged: &protocol.Op{
			Seq:     staged.Seq,
			Type:    protocol.OpWrite,
			Path:    staged.Path,
			Data:    content,
			PreHash: &newPre,
			TS:      staged.TS,
		},
	}
}

// deleteVsEditContent produces the on-disk bytes for a delete_vs_edit
// conflict: a conflict header prepended to the staged content, using the
// same format routing as overlap conflicts. Binary and sidecar-format paths
// are handled by the caller before this function is reached; only text
// formats (callout, comment, marker) are handled here.
func deleteVsEditContent(staged []byte, path string, ctx Context) []byte {
	switch chooseFormatForPath(path, ctx.DiffMode) {
	case FormatCallout:
		hdr := "> [!conflict] " + ctx.ServerKeyname + " ⟷ " + ctx.ClientKeyname + " · " + ctx.TS + "\n" +
			"> deleted by other client — your version preserved below\n" +
			"> Edit above to merge, then delete this block.\n\n"
		return append([]byte(hdr), staged...)
	case FormatComment:
		// Mirror FormatCallout: commented notice above, surviving staged content
		// LIVE below. WriteCommentBlock would invert this — it comments the body
		// (the user's work) and would need a bare uncommented placeholder for the
		// server side, breaking the syntactic-validity invariant the comment
		// format exists to uphold. The notice carries the "=== LEYLINE CONFLICT"
		// marker so conflicts.IsResolved keeps flagging the path.
		style, _ := CommentStyleForExt(extOf(path))
		notice := WriteCommentNotice(style, ctx.ServerKeyname, ctx.ClientKeyname, ctx.TS)
		content := append([]byte(notice), '\n')
		return append(content, staged...)
	default: // FormatMarker
		return []byte("<<<<<<< server (" + ctx.ServerKeyname + " · " + ctx.TS + ")\n" +
			"(deleted by other client)\n=======\n" + string(staged) + "\n>>>>>>> local\n")
	}
}

// chooseFormatForPath maps (path extension, diff_mode) to the on-disk
// conflict format. diff_mode="git" forces markers everywhere (sidecar for
// binary extensions). diff_mode="" / "leyline": .md → callout, known
// source extensions → comment block, everything else → sidecar.
func chooseFormatForPath(path, diffMode string) LogFormat {
	if diffMode == "git" {
		if isLikelyBinary(path) {
			return FormatSidecar
		}
		return FormatMarker
	}
	// leyline mode
	ext := extOf(path)
	if ext == ".md" {
		return FormatCallout
	}
	if _, ok := CommentStyleForExt(ext); ok {
		return FormatComment
	}
	return FormatSidecar
}

// extOf returns the file extension including the leading dot, or "" for
// files without an extension or where the dot is a path separator.
func extOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' {
			break
		}
	}
	return ""
}

// isText returns true if data is plausibly UTF-8 text (no NUL bytes).
func isText(data []byte) bool {
	return !bytes.Contains(data, []byte{0})
}

// isLikelyBinary returns true for extensions that are conventionally binary.
func isLikelyBinary(path string) bool {
	switch extOf(path) {
	case ".png", ".jpg", ".jpeg", ".gif", ".pdf", ".zip", ".tar", ".gz",
		".mp4", ".mp3", ".webp", ".ico", ".woff", ".woff2":
		return true
	}
	return false
}
