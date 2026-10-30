# Muse extension for the Ableton Live OSC remote script.
#
# Every write here reads the result back from the Live Object Model and
# replies with it, so the agent never has to trust a send. Addresses live
# under /live/muse/; replies go to the same address with no echoed args
# (map "skip": 0). Status replies are "ok" or "error: <why>".
from typing import Tuple, Any
import Live
from .handler import AbletonOSCHandler

MONITOR = {0: "in", 1: "auto", 2: "off"}


class MuseHandler(AbletonOSCHandler):
    def __init__(self, manager):
        super().__init__(manager)
        self.class_identifier = "muse"

    def init_api(self):
        song = lambda: self.song
        add = lambda name, fn: self.osc_server.add_handler("/live/muse/" + name, self._safe(fn))

        def track(i):
            return song().tracks[int(i)]

        # The contract speaks seconds and dB; Live speaks beats and 0..1 knobs.
        def beats(sec):
            return float(sec) * song().tempo / 60.0

        def secs(b):
            return float(b) * 60.0 / song().tempo

        def knob_for_db(prm, db):
            # Bisect the knob until its own readout matches the wanted dB.
            def db_at(v):
                txt = prm.str_for_value(v).replace("dB", "").strip()
                return -1e9 if txt.lower().startswith("-inf") else float(txt)
            lo, hi = prm.min, prm.max
            for _ in range(30):
                mid = (lo + hi) / 2
                if db_at(mid) < db:
                    lo = mid
                else:
                    hi = mid
            return hi

        # --- track state (S1, S3) -------------------------------------------
        def track_state(p: Tuple[Any]):
            t = track(p[0])
            mix = t.mixer_device
            arm = int(t.arm) if t.can_be_armed else -1
            mon = int(t.current_monitoring_state) if t.can_be_armed else -1
            clips = sum(1 for s in t.clip_slots if s.has_clip) + len(getattr(t, "arrangement_clips", []) or [])
            return (t.name, int(t.has_audio_input), arm, int(t.mute), int(t.solo), mon,
                    float(mix.volume.value), float(mix.panning.value), clips)

        def set_arm(p):
            t = track(p[0])
            if not t.can_be_armed:
                return ("error: track cannot be armed",)
            want = bool(float(p[1]))
            t.arm = want
            if t.arm != want:  # Live refuses silently, e.g. with no audio input
                return ("error: Live kept arm off; input is %s" % t.input_routing_type.display_name,)
            return (str(int(t.arm)),)

        def set_monitor(p):
            t = track(p[0])
            if not t.can_be_armed:
                return ("error: track has no monitoring",)
            t.current_monitoring_state = int(float(p[1]))
            return (str(int(t.current_monitoring_state)),)

        def inputs(p):
            t = track(p[0])
            return tuple(r.display_name for r in t.available_input_routing_types)

        def set_input(p):
            t = track(p[0])
            want = str(p[1])
            for r in t.available_input_routing_types:
                if r.display_name == want:
                    t.input_routing_type = r
                    return (t.input_routing_type.display_name,)
            names = [r.display_name for r in t.available_input_routing_types]
            try:
                t.input_routing_type = t.available_input_routing_types[int(float(want))]
                return (t.input_routing_type.display_name,)
            except (ValueError, IndexError):
                return ("error: input must be one of " + ", ".join(names),)

        # --- devices (F2) ---------------------------------------------------
        def move_device(p):
            t = track(p[0])
            dev = t.devices[int(p[1])]
            to = int(p[2])
            # move_device inserts before index `to` in the target list; moving
            # right past itself needs one more slot.
            if to > int(p[1]):
                to += 1
            song().move_device(dev, t, min(to, len(t.devices)))
            return tuple(d.name for d in t.devices)

        # --- sends and routing (S4) -----------------------------------------
        def sends(p):
            t = track(p[0])
            return tuple(float(s.value) for s in t.mixer_device.sends)

        def create_send(p):
            # Live sends go to return tracks. {to} names a return by index; a
            # missing return is created. Level is the send knob 0..1.
            t, to, level_db = track(p[0]), int(p[1]), float(p[2])
            while len(song().return_tracks) <= to:
                song().create_return_track()
            s = t.mixer_device.sends[to]
            s.value = knob_for_db(s, level_db)
            return (str(s),)

        def remove_send(p):
            s = track(p[0]).mixer_device.sends[int(p[1])]
            s.value = s.min
            return ("ok",) if s.value == s.min else ("error: send still open",)

        def set_send_value(p):
            s = track(p[0]).mixer_device.sends[int(p[1])]
            s.value = float(p[2])
            return (str(float(s.value)),)

        def output(p):
            t = track(p[0])
            return (t.output_routing_type.display_name,) + tuple(r.display_name for r in t.available_output_routing_types)

        def set_output(p):
            t = track(p[0])
            for r in t.available_output_routing_types:
                if r.display_name == str(p[1]):
                    t.output_routing_type = r
                    return (t.output_routing_type.display_name,)
            return ("error: unknown output",)

        # --- transport (S5) -------------------------------------------------
        def transport(p):
            s = song()
            return (float(s.tempo), int(s.is_playing), secs(s.current_song_time), int(s.loop),
                    secs(s.loop_start), secs(s.loop_start + s.loop_length), int(s.signature_numerator),
                    int(s.signature_denominator))

        def set_loop(p):
            s = song()
            start, end = beats(p[0]), beats(p[1])
            if end <= start:
                s.loop = False
                return ("ok",)
            s.loop_start, s.loop_length, s.loop = start, end - start, True
            return (secs(s.loop_start), secs(s.loop_start + s.loop_length))

        def seek(p):
            song().current_song_time = beats(p[0])
            return ("ok",)

        def set_signature(p):
            s = song()
            s.signature_numerator, s.signature_denominator = int(p[0]), int(p[1])
            ok = s.signature_numerator == int(p[0]) and s.signature_denominator == int(p[1])
            return ("ok",) if ok else ("error: signature not applied",)

        # --- markers (S6); positions are beats, Live's own unit --------------
        def cues(p):
            out = ()
            for i, c in enumerate(sorted(song().cue_points, key=lambda c: c.time)):
                out += (i, c.name, secs(c.time), secs(c.time), 0)
            return out

        # Live moves the play position on its next tick, so cue edits run one
        # tick later and reply from there.
        def later(address, fn):
            def run():
                try:
                    out = fn()
                except Exception as e:
                    out = ("error: %s" % e,)
                self.osc_server.send("/live/muse/" + address, out)
            self.manager.schedule_message(1, run)

        def ordered_cues():
            return sorted(song().cue_points, key=lambda c: c.time)

        def create_cue(p):
            s = song()
            start, name = beats(p[0]), str(p[3]) if len(p) > 3 else ""
            before = s.current_song_time
            s.current_song_time = start

            def done():
                if not any(abs(c.time - start) < 1e-6 for c in s.cue_points):
                    s.set_or_delete_cue()
                s.current_song_time = before
                for i, c in enumerate(ordered_cues()):
                    if abs(c.time - start) < 1e-6:
                        if name:
                            c.name = name
                        return (i,)
                return ("error: cue not created",)
            later("cue/create", done)

        def goto_cue(p):
            ordered = ordered_cues()
            i = int(p[0])
            if i >= len(ordered):
                return ("error: no such marker",)
            ordered[i].jump()
            return ("ok",)

        def delete_cue(p):
            s = song()
            ordered = ordered_cues()
            i = int(p[0])
            if i >= len(ordered):
                return ("error: no such marker",)
            t, before = ordered[i].time, s.current_song_time
            s.current_song_time = t

            def done():
                s.set_or_delete_cue()
                s.current_song_time = before
                gone = not any(abs(c.time - t) < 1e-6 for c in s.cue_points)
                return ("ok",) if gone else ("error: marker still there",)
            later("cue/delete", done)

        # --- render one track (M3): resample its post-FX output in Live -------
        # render/start records `seconds` of track's output from the song start
        # onto a temporary audio track; render/result answers "pending" until
        # the file is there, then its path. Every touched setting is restored.
        job = {}

        def restore_render_job(error=""):
            s = song()
            tmp = job.get("tmp")
            if tmp is not None and tmp in s.tracks:
                s.delete_track(list(s.tracks).index(tmp))
            restore = job.get("restore")
            if restore is not None:
                pos, loop, rec, metro = restore
                s.current_song_time, s.loop, s.record_mode, s.metronome = pos, loop, rec, metro
                s.clip_trigger_quantization = job["quant"]
            for armed_track in job.get("armed", []):
                if armed_track in s.tracks and armed_track.can_be_armed:
                    armed_track.arm = True
            job["error"] = error
            job["state"] = "done"

        def existing_audio_source(s, receiver, routes):
            route_names = {route.display_name for route in routes}
            for candidate in s.tracks:
                if candidate is receiver or not candidate.has_audio_input:
                    continue
                clips = list(getattr(candidate, "arrangement_clips", ()) or ())
                clips.extend(slot.clip for slot in candidate.clip_slots if slot.has_clip)
                if any(getattr(clip, "is_audio_clip", False) for clip in clips):
                    if candidate.name in route_names:
                        return candidate
            return None

        def render_prepare():
            s, tmp, src = song(), job["tmp"], job["src"]
            try:
                routes = list(tmp.available_input_routing_types)
                names = [route.display_name for route in routes]
                route = next((item for item in routes if item.display_name == src.name), None)
                if route is None:
                    # Live populates the receiver's track-to-track routing list
                    # asynchronously after both tracks have been created.
                    job["wait"] += 1
                    if job["wait"] < 10:  # approximately two seconds at Live's scheduler cadence
                        self.manager.schedule_message(1, render_prepare)
                        return
                    if not job.get("fallback_source_tried"):
                        job["fallback_source_tried"] = True
                        candidate = existing_audio_source(s, tmp, routes)
                        if candidate is not None:
                            job["src"] = candidate
                            self.manager.schedule_message(1, render_prepare)
                            return
                    restore_render_job("receiver cannot route source %s after two seconds; inputs are %s" %
                                       (src.name, ", ".join(names)))
                    return
                tmp.input_routing_type = route
                if tmp.input_routing_type.display_name != src.name:
                    restore_render_job("receiver input route readback differs: requested=%s read=%s" %
                                       (src.name, tmp.input_routing_type.display_name))
                    return
                channels = list(tmp.available_input_routing_channels)
                channel_names = [channel.display_name for channel in channels]
                if "Post FX" in channel_names:
                    tmp.input_routing_channel = channels[channel_names.index("Post FX")]
                tmp.current_monitoring_state = 2  # off: no feedback
                tmp.arm = True
                if not tmp.arm:
                    restore_render_job("receiver track did not arm for render")
                    return
                job["state"] = "running"
                self.manager.schedule_message(1, render_go)
            except Exception as e:
                restore_render_job("receiver setup failed: %s" % e)

        def render_go():
            s = song()
            s.clip_trigger_quantization = 0  # none: recording starts on the spot
            s.loop, s.metronome = False, False
            s.current_song_time = 0
            s.record_mode = True
            if hasattr(s, "jump_by") and abs(s.current_song_time) > 1e-3:
                s.jump_by(-s.current_song_time)
            s.start_playing()
            self.manager.schedule_message(1, render_poll)

        def render_start(p):
            s = song()
            if job.get("state") == "running":
                return ("error: a render is already running",)
            if job.get("state") == "preparing":
                return ("error: a render is being prepared",)
            if s.is_playing:
                return ("error: transport is playing",)
            src, seconds = track(p[0]), float(p[1])
            s.create_audio_track(-1)
            tmp = s.tracks[len(s.tracks) - 1]
            tmp.name = "muse-render"
            armed = [t for t in s.tracks if t is not tmp and t.can_be_armed and t.arm]
            for t in armed:
                t.arm = False
            job.clear()
            job.update(state="preparing", path="", error="", tmp=tmp, src=src,
                       end=beats(seconds), armed=armed, wait=0,
                       restore=(s.current_song_time, s.loop, s.record_mode, s.metronome),
                       quant=s.clip_trigger_quantization)
            # Live populates routings on the next update tick. The receiver is
            # created per render op and explicitly routed to this op's source.
            self.manager.schedule_message(1, render_prepare)
            return ("started",)

        def render_poll():
            s = song()
            counting = getattr(s, "is_counting_in", False)  # count-in records nothing
            if s.is_playing and (counting or s.current_song_time < job["end"]):
                self.manager.schedule_message(1, render_poll)
                return
            s.stop_playing()
            s.record_mode = False
            job["tmp"].arm = False
            job["wait"] = 0
            self.manager.schedule_message(1, render_finish)

        def render_finish():
            # Live writes the recorded clip a few ticks after the stop.
            s, tmp = song(), job["tmp"]
            clips = list(tmp.arrangement_clips)
            if not clips and job["wait"] < 30:
                job["wait"] += 1
                self.manager.schedule_message(1, render_finish)
                return
            job["path"] = clips[0].file_path if clips else ""
            if clips:
                job["span"] = (secs(clips[0].start_time), secs(clips[0].end_time), secs(s.current_song_time))
            restore_render_job("nothing was recorded" if not job["path"] else "")

        def render_result(p):
            if job.get("state") in ("preparing", "running"):
                return ("pending",)
            if job.get("error"):
                return ("error: " + job["error"],)
            if not job.get("path"):
                return ("error: no render started",)
            return (job["path"],) + job.get("span", ())

        # --- readouts (F4, F5) ----------------------------------------------
        def readout_for(p):
            # Readout a parameter would show at value v, without moving it.
            prm = track(p[0]).devices[int(p[1])].parameters[int(p[2])]
            return (prm.str_for_value(float(p[3])),)

        def value_items(p):
            prm = track(p[0]).devices[int(p[1])].parameters[int(p[2])]
            return tuple(prm.value_items) if prm.is_quantized else ()

        # --- browser presets (F10) -----------------------------------------
        # Browser items are addressed by their full category/folder path. A
        # display-name-only match can silently select the wrong item when two
        # packs contain presets with the same name.
        def browser_preset_items(target_device_name):
            browser = Live.Application.get_application().browser
            roots = ("sounds", "drums", "instruments", "audio_effects",
                     "midi_effects", "plugins", "max_for_live")
            found, visited = [], set()
            budget = [10000]

            def walk(item, path, depth, in_target=False):
                if depth > 12 or budget[0] <= 0:
                    return
                key = getattr(item, "uri", None)
                if key is not None:
                    key = str(key)
                    if key in visited:
                        return
                    visited.add(key)
                budget[0] -= 1
                children = getattr(item, "children", ()) or ()
                for child in children:
                    name = str(getattr(child, "name", ""))
                    child_path = path + (name,)
                    child_is_device = bool(getattr(child, "is_device", False))
                    child_in_target = in_target or name == target_device_name
                    if (child_in_target and getattr(child, "is_loadable", False)
                            and not child_is_device):
                        found.append(("/".join(child_path), child))
                    # Device nodes can contain browser presets even though
                    # Live does not identify those nodes as folders.
                    if (getattr(child, "is_folder", False)
                            or child_in_target or getattr(child, "children", ())):
                        walk(child, child_path, depth + 1, child_in_target)

            for root_name in roots:
                root = getattr(browser, root_name, None)
                if root is not None:
                    walk(root, (root_name,), 0)
            return found

        def preset_list(p):
            # Validate the target up front so listing and loading have the same
            # track/device addressing contract.
            target_track = track(p[0])
            device_index = int(p[1])
            if device_index < 0 or device_index >= len(target_track.devices):
                return ("error: device not found",)
            items = browser_preset_items(target_track.devices[device_index].name)
            # Leave room for OSC framing and the reply address. A large
            # browser tree must fail clearly instead of sending a truncated
            # UDP packet that looks like a partial catalog.
            labels, size = [], 0
            for label, _item in items:
                size += len(label.encode("utf-8")) + 4
                if size > 48000:
                    return ("error: preset catalog exceeds OSC reply limit",)
                labels.append(label)
            return tuple(labels)

        def preset_load(p):
            target_track = track(p[0])
            device_index, requested = int(p[1]), str(p[2])
            if device_index < 0 or device_index >= len(target_track.devices):
                return ("error: device not found",)
            matches = [(label, item) for label, item in browser_preset_items(target_track.devices[device_index].name)
                       if label == requested]
            if not matches:
                return ("error: preset not found",)
            if len(matches) != 1:
                return ("error: preset path is ambiguous",)

            app = Live.Application.get_application()
            target = target_track.devices[device_index]
            was_browsing = bool(app.view.browse_mode)
            song().view.selected_track = target_track
            song().view.select_device(target)
            try:
                if not was_browsing:
                    app.view.toggle_browse()
                app.browser.load_item(matches[0][1])
            finally:
                if not was_browsing and bool(app.view.browse_mode):
                    app.view.toggle_browse()

            # Live does not expose a canonical current-preset name for every
            # device. Read the target device back after the browser operation.
            if device_index >= len(target_track.devices):
                return ("error: Live removed the target device",)
            loaded = target_track.devices[device_index]
            # Keep the OSC response scalar-only: nested tuples are not valid
            # OSC arguments and would otherwise turn a successful load into a
            # client timeout.
            return ("ok", str(loaded.name), len(loaded.parameters))

        def midi_clip_create(p):
            target_track, slot_index, length = track(p[0]), int(p[1]), float(p[2])
            if slot_index < 0 or slot_index >= len(target_track.clip_slots):
                return ("error: clip slot out of range",)
            slot = target_track.clip_slots[slot_index]
            if slot.has_clip:
                return ("error: clip slot is occupied",)
            slot.create_clip(length)
            clip = slot.clip
            note = {"pitch": 60, "start_time": 0.0, "duration": min(0.25, length),
                    "velocity": 80, "mute": False}
            try:
                clip.add_new_notes((note,))
            except Exception:
                # Older Live builds expose set_notes instead of add_new_notes.
                clip.set_notes(((60, 0.0, min(0.25, length), 80, False),))
            if not slot.has_clip:
                return ("error: Live did not create the MIDI clip",)
            try:
                note_count = len(clip.get_notes_extended(0.0, 0, float(clip.length), 128))
            except Exception:
                note_count = len(clip.get_notes(0.0, 0, float(clip.length), 128))
            if note_count < 1:
                return ("error: MIDI clip contains no notes",)
            return ("ok", float(clip.length), note_count)

        def midi_clip_delete(p):
            target_track, slot_index = track(p[0]), int(p[1])
            if slot_index < 0 or slot_index >= len(target_track.clip_slots):
                return ("error: clip slot out of range",)
            slot = target_track.clip_slots[slot_index]
            if slot.has_clip:
                slot.delete_clip()
            return ("ok", int(slot.has_clip))

        def selected_device_get(_p):
            selected_track = song().view.selected_track
            if selected_track is None:
                return (-1, -1)
            track_index = list(song().tracks).index(selected_track)
            selected_device = selected_track.view.selected_device
            if selected_device is None:
                return (track_index, -1)
            return (track_index, list(selected_track.devices).index(selected_device))

        def selected_device_set(p):
            track_index, device_index = int(p[0]), int(p[1])
            if track_index < 0 or track_index >= len(song().tracks):
                return ("error: track index out of range",)
            selected_track = song().tracks[track_index]
            song().view.selected_track = selected_track
            # Live's Remote Script base stores Application as an object rather
            # than exposing a callable accessor. Use the public Live API getter
            # to reach the Application view, then select the device via song.view.
            Live.Application.get_application().view.show_view("Detail/DeviceChain")
            if device_index < 0:
                selected_track.view.selected_device = None
            else:
                if device_index >= len(selected_track.devices):
                    return ("error: device index out of range",)
                song().view.select_device(selected_track.devices[device_index])
            selected = selected_track.view.selected_device
            actual_index = -1 if selected is None else list(selected_track.devices).index(selected)
            return (track_index, actual_index)

        # --- capabilities ---------------------------------------------------
        ops = {
            "track/state": track_state, "track/arm": set_arm, "track/monitor": set_monitor,
            "track/inputs": inputs, "track/input": set_input,
            "device/move": move_device,
            "send/list": sends, "send/create": create_send, "send/remove": remove_send, "send/set": set_send_value,
            "track/output": output, "track/set_output": set_output,
            "transport": transport, "loop": set_loop, "seek": seek, "signature": set_signature,
            "cue/list": cues, "cue/create": create_cue, "cue/goto": goto_cue, "cue/delete": delete_cue,
            "render/start": render_start, "render/result": render_result,
            "param/readout_for": readout_for, "param/items": value_items,
            "preset/list": preset_list, "preset/load": preset_load,
            "midi_clip/create": midi_clip_create, "midi_clip/delete": midi_clip_delete,
            "view/selected_device": selected_device_get,
            "view/select_device": selected_device_set,
        }
        for name, fn in ops.items():
            add(name, fn)
        add("capabilities", lambda p: tuple(sorted(ops)))

    def _safe(self, fn):
        def run(p):
            try:
                return fn(tuple(p))
            except Exception as e:  # reply instead of going silent
                self.logger.warning("muse op failed: %s", e)
                return ("error: %s" % e,)
        return run


def attach_to_running_manager():
    """Adds MuseHandler to a running Manager without restarting Live: wraps
    Manager.init_api so every (re)load also builds this handler."""
    import gc
    for m in [o for o in gc.get_objects() if type(o).__name__ == "Manager" and hasattr(o, "handlers")]:
        cls = type(m)
        if getattr(cls, "_muse_wrapped", False):
            continue
        orig = cls.init_api

        def init_api(self, _orig=orig):
            _orig(self)
            import importlib
            from . import muse
            importlib.reload(muse)  # pick up a new muse.py on script reload
            self.handlers = [h for h in self.handlers if type(h).__name__ != "MuseHandler"]
            with self.component_guard():
                self.handlers.append(muse.MuseHandler(self))

        cls.init_api, cls._muse_wrapped = init_api, True
