package oscmap

import (
	"context"
	"errors"
	"fmt"
)

// StarterName is the track the built-in riff is put on.
const StarterName = "Guitar"

// Starter puts the riff at path on the first audio track of a set that has
// no clip at all, and names that track StarterName when the map can rename
// tracks. It loads nothing when the map cannot tell audio tracks or clips
// apart, when there is no audio track, or when any clip slot is filled: a
// set that already has audio is never touched. It returns the track index
// it loaded, or 0.
func (d *DAW) Starter(ctx context.Context, path string) (track int, why string, err error) {
	if d.M.InsertMedia == nil || len(d.M.InsertMedia.Send) == 0 ||
		len(d.M.TrackAudio.Send) == 0 || len(d.M.SceneCount.Send) == 0 || len(d.M.HasClip.Send) == 0 {
		return 0, "the map cannot tell audio tracks or clips apart (needs track_audio, scene_count, has_clip and insert_media)", nil
	}
	tracks, err := d.Tracks(ctx)
	if err != nil {
		return 0, "", err
	}
	first := 0
	for _, t := range tracks {
		if t.Audio {
			first = t.Index
			break
		}
	}
	if first == 0 {
		return 0, "no track reports audio (track_audio is false everywhere)", nil
	}
	r, err := d.ask(ctx, d.M.SceneCount, vars{})
	if err != nil || len(r) == 0 {
		return 0, "", fmt.Errorf("scene count: %v", err)
	}
	scenes, _ := num(r[0])
	for _, t := range tracks {
		for s := 0; s < int(scenes); s++ {
			r, err := d.ask(ctx, d.M.HasClip, vars{track: t.Index - 1 + d.M.Base, slot: s + d.M.Base})
			if err != nil || len(r) == 0 {
				return 0, "", fmt.Errorf("clip check on track %d: %v", t.Index, err)
			}
			if b, ok := asBool(r[0]); ok {
				if b {
					return 0, fmt.Sprintf("track %d slot %d already has a clip, so the set has audio", t.Index, s+1), nil
				}
			} else if n, _ := num(r[0]); n != 0 {
				return 0, fmt.Sprintf("track %d slot %d already has a clip, so the set has audio", t.Index, s+1), nil
			}
		}
	}
	if err := d.InsertMedia(ctx, first, path, 0); err != nil {
		return 0, "", err
	}
	if d.M.RenameTrack != nil && len(d.M.RenameTrack.Send) > 0 {
		if err := d.RenameTrack(ctx, first, StarterName); err != nil {
			return first, "", errors.Join(errors.New("riff loaded, but the rename failed"), err)
		}
	}
	return first, "", nil
}
