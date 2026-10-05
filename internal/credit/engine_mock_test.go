package credit_test

import (
	"context"
	"math/big"
	"testing"

	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/credit/mocks"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// runInTx makes MockStore invoke the transaction body against txMock.
func runInTx(txMock *mocks.MockTx) func(context.Context, func(context.Context, credit.Tx) error) error {
	return func(ctx context.Context, fn func(context.Context, credit.Tx) error) error {
		return fn(ctx, txMock)
	}
}

func TestApplyUnknownDeposit(t *testing.T) {
	txMock := mocks.NewMockTx(t)
	txMock.On("DepositForUpdate", mock.Anything, "evm:0xdead:native").
		Return(credit.View{}, errs.ErrDepositNotFound)

	store := mocks.NewMockStore(t)
	store.On("InTx", mock.Anything, mock.Anything).Return(runInTx(txMock))

	err := credit.NewEngine(store).Apply(context.Background(), "evm:0xdead:native", domain.EventDepthReached)
	require.ErrorIs(t, err, errs.ErrDepositNotFound)
}

// A client retry with the same ref succeeds silently.
func TestDebitDuplicateRefIsIdempotent(t *testing.T) {
	txMock := mocks.NewMockTx(t)
	txMock.On("BalanceForUpdate", mock.Anything, "alice", "ETH").
		Return(credit.Balance{Amount: big.NewInt(100), Held: new(big.Int)}, nil)
	txMock.On("InsertEntry", mock.Anything, mock.Anything).
		Return(errs.ErrDuplicateRef)

	store := mocks.NewMockStore(t)
	store.On("InTx", mock.Anything, mock.Anything).Return(runInTx(txMock))

	err := credit.NewEngine(store).Debit(context.Background(), "alice", "ETH", big.NewInt(100), "withdrawal:1")
	require.NoError(t, err)
	txMock.AssertNotCalled(t, "SetBalance")
}
