package internal

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/yuvraajnarula/pocketledger/services/ledger-svc/internal/walletsvc"
)

type Reconciler struct {
	db           *sql.DB
	repo         *LedgerRepo
	walletClient *walletsvc.Client
	metrics      *Metrics
}

func NewReconciler(db *sql.DB, walletClient *walletsvc.Client, metrics *Metrics) *Reconciler {
	return &Reconciler{
		db:           db,
		repo:         NewLedgerRepo(db),
		walletClient: walletClient,
		metrics:      metrics,
	}
}

// Run starts the reconciliation loop. It blocks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	slog.Info("reconciler started")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("reconciler stopped")
			return
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

// maxReconcileAttempts is the dead-letter cap: after this many failed retries
// a deposit stops being rescheduled and needs manual intervention.
const maxReconcileAttempts = 15

func (r *Reconciler) reconcile(ctx context.Context) {
	r.metrics.RecordReconcileRun()

	entries, err := r.repo.GetUnreconciledDeposits(ctx)
	if err != nil {
		slog.Error("reconciler: failed to fetch unreconciled entries", "error", err)
		return
	}

	// Update gauge with current count (even if 0)
	r.metrics.SetUnreconciledCount(int64(len(entries)))

	if len(entries) == 0 {
		return
	}

	if r.walletClient == nil {
		slog.Error("reconciler: no wallet-svc client configured; cannot reconcile deposits")
		return
	}

	slog.Info("reconciler: processing entries", "count", len(entries))

	for _, entry := range entries {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		err := r.walletClient.Deposit(reqCtx, entry.WalletID, entry.Amount, entry.ReferenceID)
		cancel()

		if err != nil {
			slog.Error("reconciler: deposit still failing",
				"wallet", entry.WalletID,
				"amount", entry.Amount,
				"ref", entry.ReferenceID,
				"error", err,
			)
			_ = r.repo.IncReconcileAttempts(ctx, entry.ReferenceID)

			if entry.Attempts+1 >= maxReconcileAttempts {
				slog.Warn("reconciler: dead-letter — deposit exceeded retry cap, manual intervention required",
					"wallet", entry.WalletID,
					"amount", entry.Amount,
					"ref", entry.ReferenceID,
					"attempts", entry.Attempts+1,
				)
			}
			r.metrics.IncReconciledErrors()
			continue
		}

		if err := r.repo.MarkReconciled(ctx, entry.ReferenceID); err != nil {
			slog.Error("reconciler: failed to mark reconciled",
				"ref", entry.ReferenceID, "error", err)
			r.metrics.IncReconciledErrors()
			continue
		}

		r.metrics.IncReconciled()
		slog.Info("reconciler: deposit reconciled",
			"wallet", entry.WalletID,
			"amount", entry.Amount,
			"ref", entry.ReferenceID,
		)
	}
}
