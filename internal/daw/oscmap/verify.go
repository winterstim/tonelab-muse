package oscmap

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Check is one verified command, with what the DAW actually answered so a
// failure can be fixed from evidence.
type Check struct {
	Role   string `json:"role"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Verify runs every command of the map against the running DAW. The one
// write it makes (moving a parameter to mid-range) is put back.
func Verify(ctx context.Context, d *DAW) []Check {
	var out []Check
	add := func(role string, ok bool, f string, a ...any) bool {
		out = append(out, Check{role, ok, fmt.Sprintf(f, a...)})
		return ok
	}
	m := d.M
	if r, err := d.ask(ctx, m.Ping, vars{}); !add("ping", err == nil, "reply %v err %v", r, err) {
		return out
	}
	r, err := d.ask(ctx, m.TrackCount, vars{})
	n := 0.0
	if err == nil && len(r) > 0 {
		n, _ = num(r[0])
	}
	if !add("track_count", err == nil && n > 0, "reply %v err %v", r, err) {
		return out
	}
	r, err = d.ask(ctx, m.TrackName, vars{track: m.Base})
	add("track_name", err == nil && len(r) > 0, "first track: reply %v err %v", r, err)

	// find a track with a device to test parameters on
	track, fx := -1, -1
	var devs []any
	for t := 0; t < int(n) && track < 0; t++ {
		devs, err = d.ask(ctx, m.DeviceNames, vars{track: t + m.Base})
		if err == nil && len(devs) > 0 {
			track, fx = t+m.Base, m.Base
		}
	}
	if track < 0 && n > 0 && m.Chains() {
		// an empty project: borrow one device from the catalog to test on,
		// and take it off again at the end
		if cat, cerr := d.ask(ctx, m.DeviceCatalog, vars{track: m.Base}); cerr == nil && len(cat) > 0 {
			t := m.Base
			before := append([]any(nil), devs...)
			name := fmt.Sprint(cat[0])
			r, _ := d.ask(ctx, m.InsertDevice, vars{track: t, fx: m.Base, name: name})
			after, rerr := d.ask(ctx, m.DeviceNames, vars{track: t})
			idx, inserted, nameMatches := insertedDevice(before, after, name)
			if rerr == nil && inserted && nameMatches {
				devs, err = after, nil
				track, fx = t, idx+m.Base
				defer cleanupDevice(d, track, fx)
			} else if inserted || rerr != nil {
				at := insertedReplyIndex(r, m.Base, m.Base, len(before))
				if inserted {
					at = idx + m.Base
				}
				_ = cleanupDevice(d, t, at)
				devs, err = after, rerr
			} else {
				devs, err = after, rerr
			}
		}
	}
	if !add("device_names", track >= 0, "track %d: %v (err %v)", track, trim(devs), err) {
		return out
	}
	v := vars{track: track, fx: fx}
	names, err := d.ask(ctx, m.ParamNames, v)
	if !add("param_names", err == nil && len(names) > 0, "track %d device %d: reply %v err %v", track, fx, trim(names), err) {
		return out
	}
	vals, err := d.ask(ctx, m.ParamValues, v)
	add("param_values", err == nil && len(vals) == len(names), "%d names, %d values: %v err %v", len(names), len(vals), trim(vals), err)
	for _, role := range []string{"param_mins", "param_maxs"} {
		c := m.commands()[role]
		if len(c.Send) == 0 {
			continue
		}
		r, err := d.ask(ctx, c, v)
		add(role, err == nil && len(r) == len(names), "%d names, %d entries: %v err %v", len(names), len(r), trim(r), err)
	}
	// a continuous parameter: pick the first whose range is wider than 1 step
	mins := d.list(ctx, m.ParamMins, v, len(names), 0)
	maxs := d.list(ctx, m.ParamMaxs, v, len(names), 1)
	p := -1
	for i := range names {
		if i > 0 && maxs[i]-mins[i] > 0 { // index 0 is often on/off
			p = i
			break
		}
	}
	if p < 0 {
		p = 0
	}
	v.param = p + m.Base
	cur, err := d.ask(ctx, m.GetParam, v)
	c0, ok := 0.0, false
	if err == nil && len(cur) > 0 {
		c0, ok = num(cur[0])
	}
	if !add("get_param", ok, "param %d (%v): reply %v err %v", p, names[p], cur, err) {
		return out
	}
	mid := (mins[p] + maxs[p]) / 2
	if math.Abs(mid-c0) < 1e-6 {
		mid = mins[p] + (maxs[p]-mins[p])*0.3
	}
	v.value = mid
	_, err = d.ask(ctx, m.SetParam, v)
	after, err2 := d.ask(ctx, m.GetParam, v)
	got, _ := 0.0, false
	if err2 == nil && len(after) > 0 {
		got, _ = num(after[0])
	}
	moved := err == nil && math.Abs(got-mid) <= math.Max(1e-3, (maxs[p]-mins[p])*0.02)
	add("set_param", moved, "set %v to %.4g, read back %v (err %v / %v)", names[p], mid, after, err, err2)
	v.value = c0
	d.ask(ctx, m.SetParam, v) // put it back
	if len(m.ParamText.Send) > 0 {
		r, err := d.ask(ctx, m.ParamText, v)
		add("param_text", err == nil && len(r) > 0, "reply %v err %v", r, err)
	}
	if m.Shows() {
		verifyShow(ctx, d, vars{track: track, fx: fx}, len(names), add)
	}
	if m.Chains() {
		verifyChain(ctx, d, int(n)-1+m.Base, add)
	}
	return out
}

// verifyChain adds one device from the catalog at the end of a track,
// checks it is there, takes it off again and checks the chain is back.
func verifyChain(ctx context.Context, d *DAW, track int, add func(string, bool, string, ...any) bool) {
	m := d.M
	cat, err := d.ask(ctx, m.DeviceCatalog, vars{track: track})
	if !add("device_catalog", err == nil && len(cat) > 0, "%d names: %v err %v", len(cat), trim(cat), err) {
		return
	}
	before, err := d.ask(ctx, m.DeviceNames, vars{track: track})
	if err != nil {
		add("insert_device", false, "could not read track %d's devices first: %v", track, err)
		return
	}
	name := fmt.Sprint(cat[0])
	requested := len(before) + m.Base
	r, insertErr := d.ask(ctx, m.InsertDevice, vars{track: track, fx: requested, name: name})
	after, readErr := d.ask(ctx, m.DeviceNames, vars{track: track})
	index, inserted, nameMatches := insertedDevice(before, after, name)
	verified := readErr == nil && len(after) == len(before)+1 && inserted && nameMatches
	add("insert_device", insertErr == nil && verified, "requested %q at device %d on track %d: reply %v, devices %d → %d, inserted %v, name matches %v, err %v / readback %v", name, requested, track, r, len(before), len(after), inserted, nameMatches, insertErr, readErr)

	// A failed reply or postcondition does not mean the write had no effect.
	// When the reread shows the added slot, remove that exact slot. If the
	// reread itself failed, try the returned index when valid and otherwise
	// the requested end position because the insert command may have taken effect.
	removeAt, cleanup := -1, false
	if inserted {
		removeAt, cleanup = index+m.Base, true
	} else if readErr != nil {
		// The command was sent but the chain could not be checked. Make a
		// best-effort cleanup at the returned slot or requested insertion slot.
		removeAt, cleanup = insertedReplyIndex(r, m.Base, requested, len(before)), true
	}
	if !cleanup {
		return
	}
	removeErr := cleanupDevice(d, track, removeAt)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	back, backErr := d.ask(cleanupCtx, m.DeviceNames, vars{track: track})
	removed := backErr == nil && sameDevices(before, back)
	add("remove_device", removeErr == nil && removed, "removed device %d: devices %d → %d, restored original chain %v, err %v / readback %v", removeAt, len(after), len(back), removed, removeErr, backErr)
}

func cleanupDevice(d *DAW, track, fx int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := d.ask(ctx, d.M.RemoveDevice, vars{track: track, fx: fx})
	return err
}

// insertedDevice identifies the one chain position whose removal restores
// the prior list. The reply is intentionally not used as the source of truth.
func insertedDevice(before, after []any, name string) (index int, inserted, nameMatches bool) {
	if len(after) != len(before)+1 {
		return -1, false, false
	}
	for i := range after {
		if sameDevices(before, append(append([]any(nil), after[:i]...), after[i+1:]...)) {
			return i, true, fmt.Sprint(after[i]) == name
		}
	}
	return -1, false, false
}

func sameDevices(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if fmt.Sprint(a[i]) != fmt.Sprint(b[i]) {
			return false
		}
	}
	return true
}

func insertedReplyIndex(reply []any, base, requested, beforeCount int) int {
	if len(reply) > 0 {
		if n, ok := num(reply[0]); ok && n >= float64(base) && n <= float64(beforeCount+base) {
			return int(n)
		}
	}
	return requested
}

// verifyShow shows a device and checks it answered without error and left
// every parameter where it was.
func verifyShow(ctx context.Context, d *DAW, v vars, n int, add func(string, bool, string, ...any) bool) {
	m := d.M
	before := d.list(ctx, m.ParamValues, v, n, 0)
	r, err := d.ask(ctx, m.ShowDevice, v)
	after := d.list(ctx, m.ParamValues, v, n, 0)
	same := true
	for i := range before {
		if math.Abs(before[i]-after[i]) > 1e-6 {
			same = false
		}
	}
	add("show_device", err == nil && same, "track %d device %d: reply %v err %v, parameters unchanged %v", v.track, v.fx, trim(r), err, same)
}

func trim(xs []any) []any {
	if len(xs) > 8 {
		return append(xs[:8:8], "…")
	}
	return xs
}

// Passed reports whether every check is OK.
func Passed(cs []Check) bool {
	for _, c := range cs {
		if !c.OK {
			return false
		}
	}
	return len(cs) > 0
}
