package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authante "github.com/cosmos/cosmos-sdk/x/auth/ante"
	"github.com/palantir/stacktrace"

	evmtypes "github.com/tharsis/ethermint/x/evm/types"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/ethereum/go-ethereum/core"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// AccountKeeper defines an expected keeper interface for the auth module's AccountKeeper
type AccountKeeper interface {
	authante.AccountKeeper
	NewAccountWithAddress(ctx sdk.Context, addr sdk.AccAddress) authtypes.AccountI
	GetSequence(sdk.Context, sdk.AccAddress) (uint64, error)
}

// BankKeeper defines an expected keeper interface for the bank module's Keeper
type BankKeeper interface {
	authtypes.BankKeeper
	GetBalance(ctx sdk.Context, addr sdk.AccAddress, denom string) sdk.Coin
}

// DeductTxCostsFromUserBalance it calculates the tx costs and deducts the fees
func DeductTxCostsFromUserBalance(
	ctx sdk.Context,
	bankKeeper BankKeeper,
	accountKeeper AccountKeeper,
	msgEthTx evmtypes.MsgEthereumTx,
	txData evmtypes.TxData,
	denom string,
	homestead bool,
	istanbul bool,
) error {
	isContractCreation := txData.GetTo() == nil

	// fetch sender account from signature
	signerAcc, err := authante.GetSignerAcc(ctx, accountKeeper, msgEthTx.GetFrom())
	if err != nil {
		return stacktrace.Propagate(err, "account not found for sender %s", msgEthTx.From)
	}

	gasLimit := txData.GetGas()

	var accessList ethtypes.AccessList
	if txData.GetAccessList() != nil {
		accessList = txData.GetAccessList()
	}

	intrinsicGas, err := core.IntrinsicGas(txData.GetData(), accessList, isContractCreation, homestead, istanbul)
	if err != nil {
		return stacktrace.Propagate(
			sdkerrors.Wrap(err, "failed to compute intrinsic gas cost"),
			"failed to retrieve intrinsic gas, contract creation = %t; homestead = %t, istanbul = %t", isContractCreation, homestead, istanbul)
	}

	// intrinsic gas verification during CheckTx
	if ctx.IsCheckTx() && gasLimit < intrinsicGas {
		return sdkerrors.Wrapf(sdkerrors.ErrOutOfGas, "gas limit too low: %d (gas limit) < %d (intrinsic gas)", gasLimit, intrinsicGas)
	}

	// calculate the fees paid to validators based on gas limit and price
	feeAmt := txData.Fee() // fee = gas limit * gas price

	fees := sdk.Coins{sdk.NewCoin(denom, sdk.NewIntFromBigInt(feeAmt))}

	// deduct the full gas cost from the user balance
	if err := authante.DeductFees(bankKeeper, ctx, signerAcc, fees); err != nil {
		return stacktrace.Propagate(
			err,
			"failed to deduct full gas cost %s from the user %s balance", fees, msgEthTx.From,
		)
	}
	return nil
}

// CheckSenderBalance validates sender has enough funds to pay for tx cost
func CheckSenderBalance(
	ctx sdk.Context,
	bankKeeper BankKeeper,
	sender sdk.AccAddress,
	txData evmtypes.TxData,
	denom string,
) error {
	balance := bankKeeper.GetBalance(ctx, sender, denom)
	cost := txData.Cost()

	if balance.Amount.BigInt().Cmp(cost) < 0 {
		return stacktrace.Propagate(
			sdkerrors.Wrapf(
				sdkerrors.ErrInsufficientFunds,
				"sender balance < tx cost (%s < %s%s)", balance, txData.Cost(), denom,
			),
			"sender should have had enough funds to pay for tx cost = fee + amount (%s = %s + %s)", cost, txData.Fee(), txData.GetValue(),
		)
	}
	return nil
}
