# REAPER live conformance

Captured 2026-11-03; REAPER was open. The record-input live probe is disabled after REAPER stopped responding. It is routed to three-step user guidance instead.

Result: 37 pass / 0 fail / 4 fallback / 0 unsupported (41 operations).

Fallback rows remain limited to operations with missing APIs or pending PR #22.

| op | status | reason |
|---|---|---|
| list_tracks | pass | readback matched |
| manage_track | pass | readback matched |
| set_track_prop | pass | readback matched |
| route_send | pass | readback matched |
| set_transport | pass | readback matched |
| markers | pass | readback matched |
| undo | pass | readback matched |
| save_project | fallback | missing DAW API call: OS-backed project-save routing is pending PR #22 |
| insert_media | pass | readback matched |
| starter_riff | pass | readback matched |
| render_track | pass | readback matched |
| import_reference | pass | readback matched |
| record_input | fallback | live probe disabled after REAPER stopped responding; guidance: 1. arm the track 2. select the input 3. press record |
| catalog_fx | pass | readback matched |
| manage_fx | pass | readback matched |
| show_fx_ui | pass | readback matched |
| read_params | pass | readback matched |
| set_param | pass | readback matched |
| learn_param | fallback | missing DAW API call: parameter mapping learn/unlearn endpoint is absent |
| project_state | pass | readback matched |
| verify_write | pass | readback matched |
| model_switch | fallback | missing DAW API call: current model readback/restore token for the selected slot is absent |
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
