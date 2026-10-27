package core

import "context"

// ContractResult is the standard response for contract operations.
// Every write operation returns {applied, readback}.
type ContractResult struct {
	Applied  any `json:"applied"`
	Readback any `json:"readback"`
}

// ContractExecutor executes an operation defined by the contract.
type ContractExecutor interface {
	Execute(ctx context.Context, op string, args any) (ContractResult, error)
}

// Canonical contract operation names covering the capability IDs in the specification.
const (
	OpCapabilities      = "capabilities"
	OpListTracks        = "list_tracks"       // S1
	OpManageTrack       = "manage_track"      // S2
	OpSetTrackProp      = "set_track_prop"    // S3
	OpRouteSend         = "route_send"        // S4
	OpSetTransport      = "set_transport"     // S5
	OpMarkers           = "markers"           // S6
	OpUndo              = "undo"              // S7
	OpSaveProject       = "save_project"      // S8
	OpInsertMedia       = "insert_media"      // M1
	OpStarterRiff       = "starter_riff"      // M2
	OpRenderTrack       = "render_track"      // M3
	OpImportReference   = "import_reference"  // M4
	OpRecordInput       = "record_input"      // M5
	OpCatalogFX         = "catalog_fx"        // F1
	OpManageFX          = "manage_fx"         // F2
	OpShowFXUI          = "show_fx_ui"        // F3
	OpReadParams        = "read_params"       // F4
	OpSetParam          = "set_param"         // F5
	OpLearnParam        = "learn_param"       // F6
	OpProjectState      = "project_state"     // F7
	OpVerifyWrite       = "verify_write"      // F8
	OpModelSwitch       = "model_switch"      // F9
	OpPresets           = "presets"           // F10
	OpGuideUser         = "guide_user"        // F11
	OpMeasureAudio      = "measure_audio"     // A1
	OpMatchLoudness     = "match_loudness"    // A2
	OpSpectrogram       = "spectrogram"       // A3
	OpMixClash          = "mix_clash"         // A4
	OpReferenceMatch    = "reference_match"   // A5
	OpDiagnoseSilence   = "diagnose_silence"  // A6
	OpSetMode           = "set_mode"          // G1
	OpSnapshot          = "snapshot"          // G2
	OpDelegateTask      = "delegate_task"     // G3
	OpTokenBudget       = "token_budget"      // G4
	OpCallGuard         = "call_guard"        // G5
	OpTasteMemory       = "taste_memory"      // G6
	OpProcessInput      = "process_input"     // I1
	OpOnboardDAW        = "onboard_daw"       // I2
	OpPlatformSetup     = "platform_setup"    // I3
	OpUIState           = "ui_state"          // I4
	OpReleaseSpec       = "release_spec"      // I5
	OpPluginWindows     = "plugin_windows"
	OpListPluginWindows = "list_plugin_windows"
)

// PluginWindow describes one open plugin window in the DAW.
type PluginWindow struct {
	Title     string `json:"title"`
	OwningApp string `json:"owning_app"`
}

// PluginWindowLister lists open plugin windows in the DAW.
type PluginWindowLister interface {
	ListPluginWindows(ctx context.Context) ([]PluginWindow, error)
}

// CapabilityIDs contains the 41 capability IDs defined in the contract.
var CapabilityIDs = []string{
	"S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8",
	"M1", "M2", "M3", "M4", "M5",
	"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11",
	"A1", "A2", "A3", "A4", "A5", "A6",
	"G1", "G2", "G3", "G4", "G5", "G6",
	"I1", "I2", "I3", "I4", "I5",
}

// ContractOperations is the canonical list of operations covered by the 41
// capability IDs. The capabilities discovery operation is meta and excluded.
var ContractOperations = []string{
	OpListTracks, OpManageTrack, OpSetTrackProp, OpRouteSend, OpSetTransport,
	OpMarkers, OpUndo, OpSaveProject, OpInsertMedia, OpStarterRiff,
	OpRenderTrack, OpImportReference, OpRecordInput, OpCatalogFX, OpManageFX,
	OpShowFXUI, OpReadParams, OpSetParam, OpLearnParam, OpProjectState,
	OpVerifyWrite, OpModelSwitch, OpPresets, OpGuideUser, OpMeasureAudio,
	OpMatchLoudness, OpSpectrogram, OpMixClash, OpReferenceMatch,
	OpDiagnoseSilence, OpSetMode, OpSnapshot, OpDelegateTask, OpTokenBudget,
	OpCallGuard, OpTasteMemory, OpProcessInput, OpOnboardDAW,
	OpPlatformSetup, OpUIState, OpReleaseSpec,
}
