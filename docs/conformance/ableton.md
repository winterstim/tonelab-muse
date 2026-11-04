# Ableton Live live conformance

The render check uses a temporary conformance track with a short clip. Rendering, riff creation, and preset restore pass.

Result: 38 pass / 3 guided / 0 fail (41 actions).

Guided rows identify operations completed with brief user assistance.

| op | status | reason |
|---|---|---|
| list_tracks | pass | readback matched |
| manage_track | pass | readback matched |
| set_track_prop | pass | requested=0.8 read=0.800000011920929 |
| route_send | pass | readback matched |
| set_transport | pass | readback matched |
| markers | pass | readback matched |
| undo | pass | readback matched |
| save_project | pass | readback matched |
| insert_media | pass | readback matched |
| starter_riff | pass | readback matched |
| render_track | pass | readback matched |
| import_reference | pass | readback matched |
| record_input | guided | arm a track, select an input, then record |
| catalog_fx | pass | readback matched |
| manage_fx | pass | readback matched |
| show_fx_ui | pass | selected target device readback matched |
| read_params | pass | readback matched |
| set_param | pass | readback matched |
| learn_param | guided | use Live’s parameter mapping controls |
| project_state | pass | readback matched |
| verify_write | pass | readback matched |
| model_switch | guided | choose the amp model in its plug-in interface |
| presets | pass | readback matched |
| guide_user | pass | local contract executor readback matched |
| measure_audio | pass | local contract executor readback matched |
| match_loudness | pass | local contract executor readback matched |
| spectrogram | pass | local contract executor readback matched |
| mix_clash | pass | local contract executor readback matched |
| reference_match | pass | local contract executor readback matched |
| diagnose_silence | pass | local contract executor readback matched |
| set_mode | pass | local contract executor readback matched |
| snapshot | pass | local contract executor readback matched |
| delegate_task | pass | local contract executor readback matched |
| token_budget | pass | local contract executor readback matched |
| call_guard | pass | local contract executor readback matched |
| taste_memory | pass | local contract executor readback matched |
| process_input | pass | local contract executor readback matched |
| onboard_daw | pass | local contract executor readback matched |
| platform_setup | pass | local contract executor readback matched |
| ui_state | pass | local contract executor readback matched |
| release_spec | pass | local contract executor readback matched |
