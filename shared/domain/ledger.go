package domain

func ComputeDeposit(destID string, amount int64) ([]LedgerEntry, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	return []LedgerEntry{
		{WalletID: destID, EntryType: "CREDIT", Amount: amount},
	}, nil
}

func ComputeWithdraw(srcID string, balance, amount int64) ([]LedgerEntry, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if balance < amount {
		return nil, ErrInsufficientFunds
	}
	return []LedgerEntry{
		{WalletID: srcID, EntryType: "DEBIT", Amount: amount},
	}, nil
}

func ComputeTransfer(srcID, destID string, srcBalance, amount int64) ([]LedgerEntry, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if srcBalance < amount {
		return nil, ErrInsufficientFunds
	}
	return []LedgerEntry{
		{WalletID: srcID, EntryType: "DEBIT", Amount: amount},
		{WalletID: destID, EntryType: "CREDIT", Amount: amount},
	}, nil
}
