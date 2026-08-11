package session

// State is the derived lifecycle state of a session. It is never stored
// anywhere — always recomputed from the claim + sandbox.
type State string

const (
	// StatePending — claim exists but no sandbox is bound yet.
	StatePending State = "Pending"
	// StateReady — operatingMode Running and the sandbox reports Ready.
	StateReady State = "Ready"
	// StateWaking — operatingMode Running but not Ready yet (first boot or
	// resume in progress).
	StateWaking State = "Waking"
	// StateSuspended — operatingMode Suspended, pod confirmed gone.
	StateSuspended State = "Suspended"
	// StateSuspending — operatingMode Suspended, pod still terminating.
	StateSuspending State = "Suspending"
	// StateTerminating — the claim is being deleted.
	StateTerminating State = "Terminating"
)

// Facts are the observed inputs to the state derivation.
type Facts struct {
	// Terminating is true when the claim has a deletionTimestamp.
	Terminating bool
	// HasSandbox is true when a Sandbox is bound and readable.
	HasSandbox bool
	// OperatingMode is sandbox.spec.operatingMode ("Running"/"Suspended";
	// empty defaults to Running, matching the CRD default).
	OperatingMode string
	// Ready is the sandbox's Ready condition == True.
	Ready bool
	// Suspended is the sandbox's Suspended condition == True.
	Suspended bool
}

// Derive maps observed facts to the session state (docs/architecture.md table).
func Derive(f Facts) State {
	switch {
	case f.Terminating:
		return StateTerminating
	case !f.HasSandbox:
		return StatePending
	case f.OperatingMode == "Suspended" && f.Suspended:
		return StateSuspended
	case f.OperatingMode == "Suspended":
		return StateSuspending
	case f.Ready:
		return StateReady
	default:
		return StateWaking
	}
}
