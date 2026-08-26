package usage

const progressRecordNotifyBatch int64 = 256

// ScanProgress is a point-in-time, monotonic snapshot of scanner work.
type ScanProgress struct {
	HomesTotal       int   `json:"homes_total"`
	HomesDiscovered  int   `json:"homes_discovered"`
	FilesDiscovered  int64 `json:"files_discovered"`
	FilesProcessed   int64 `json:"files_processed"`
	RecordsProcessed int64 `json:"records_processed"`
	EventsInserted   int64 `json:"events_inserted"`
	Warnings         int64 `json:"warnings"`
}

// ProgressObserver receives value snapshots synchronously while the scanner
// lock is held. Observers must not call back into the same Scanner.
type ProgressObserver func(ScanProgress)

type progressReporter struct {
	observer           ProgressObserver
	snapshot           ScanProgress
	recordsSinceNotify int64
}

func newProgressReporter(homesTotal int, observer ProgressObserver) *progressReporter {
	if observer == nil {
		return nil
	}
	return &progressReporter{
		observer: observer,
		snapshot: ScanProgress{HomesTotal: homesTotal},
	}
}

func (r *progressReporter) notify() {
	if r == nil {
		return
	}
	r.observer(r.snapshot)
	r.recordsSinceNotify = 0
}

func (r *progressReporter) homeDiscovered() {
	if r == nil {
		return
	}
	r.snapshot.HomesDiscovered++
	r.notify()
}

func (r *progressReporter) fileDiscovered() {
	if r == nil {
		return
	}
	r.snapshot.FilesDiscovered++
	r.notify()
}

func (r *progressReporter) fileProcessed() {
	if r == nil {
		return
	}
	r.snapshot.FilesProcessed++
	r.notify()
}

func (r *progressReporter) recordProcessed() {
	if r == nil {
		return
	}
	r.snapshot.RecordsProcessed++
	r.recordsSinceNotify++
	if r.recordsSinceNotify >= progressRecordNotifyBatch {
		r.notify()
	}
}

func (r *progressReporter) addOutcome(eventsInserted, warnings int64) {
	if r == nil || (eventsInserted == 0 && warnings == 0) {
		return
	}
	r.snapshot.EventsInserted += eventsInserted
	r.snapshot.Warnings += warnings
	r.notify()
}
