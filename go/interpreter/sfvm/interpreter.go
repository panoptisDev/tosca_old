// Copyright (c) 2025 Sonic Operations Ltd
//
// Use of this software is governed by the Business Source License included
// in the LICENSE file and at soniclabs.com/bsl11.
//
// Change Date: 2028-4-16
//
// On the date above, in accordance with the Business Source License, use of
// this software will be governed by the GNU Lesser General Public License v3.

package sfvm

import (
	"fmt"

	"github.com/0xsoniclabs/tosca/go/tosca"
	"github.com/0xsoniclabs/tosca/go/tosca/vm"
)

// status is enumeration of the execution state of an interpreter run.
type status byte

const (
	statusRunning        status = iota // < all fine, ops are processed
	statusStopped                      // < execution stopped with a STOP
	statusReverted                     // < execution stopped with a REVERT
	statusReturned                     // < execution stopped with a RETURN
	statusSelfDestructed               // < execution stopped with a SELF-DESTRUCT
	statusFailed                       // < execution stopped with a logic error
)

// context is the execution environment of an interpreter run. It contains all
// the necessary state to execute a contract, including input parameters, the
// contract code, and internal execution state such as the program counter,
// stack, and memory. For each contract execution, a new context is created.
type context struct {
	// Inputs
	params   tosca.Parameters
	context  tosca.RunContext
	code     tosca.Code
	analysis jumpDestMap

	// Execution state
	pc     int32
	gas    tosca.Gas
	refund tosca.Gas
	stack  *stack
	memory *Memory

	// Intermediate data
	returnData []byte // < the result of the last nested contract call

	// Configuration flags
	withShaCache bool
}

// useGas reduces the gas level by the given amount. If the gas level drops
// below zero, the caller should stop the execution with an error status. The function
// returns true if sufficient gas was available and execution can continue,
// false otherwise.
func (c *context) useGas(amount tosca.Gas) error {
	if c.gas < 0 || amount < 0 || c.gas < amount {
		return errOutOfGas
	}
	c.gas -= amount
	return nil
}

// isAtLeast returns true if the interpreter is is running at least at the given
// revision or newer, false otherwise.
func (c *context) isAtLeast(revision tosca.Revision) bool {
	return c.params.Revision >= revision
}

func run(
	analysis analysis,
	config config,
	params tosca.Parameters,
) (tosca.Result, error) {
	// Don't bother with the execution if there's no code.
	if len(params.Code) == 0 {
		return tosca.Result{
			Output:  nil,
			GasLeft: params.Gas,
			Success: true,
		}, nil
	}

	// Set up execution context.
	var ctxt = context{
		params:       params,
		context:      params.Context,
		gas:          params.Gas,
		stack:        NewStack(),
		memory:       NewMemory(),
		code:         params.Code,
		analysis:     *analysis.analyzeJumpDest(params.Code, params.CodeHash),
		withShaCache: config.withShaCache,
	}
	defer ReturnStack(ctxt.stack)

	status := execute(&ctxt, false)
	return generateResult(status, &ctxt)
}

func generateResult(status status, ctxt *context) (tosca.Result, error) {
	// Handle return status
	switch status {
	case statusStopped, statusSelfDestructed:
		return tosca.Result{
			Success:   true,
			GasLeft:   ctxt.gas,
			GasRefund: ctxt.refund,
		}, nil
	case statusReturned:
		return tosca.Result{
			Success:   true,
			Output:    ctxt.returnData,
			GasLeft:   ctxt.gas,
			GasRefund: ctxt.refund,
		}, nil
	case statusReverted:
		return tosca.Result{
			Success: false,
			Output:  ctxt.returnData,
			GasLeft: ctxt.gas,
		}, nil
	case statusFailed:
		return tosca.Result{
			Success: false,
		}, nil
	default:
		return tosca.Result{}, fmt.Errorf("unexpected error in interpreter, unknown status: %v", status)
	}
}

// --- Execution ---

// execute runs the contract code in the given context. If oneStepOnly is true,
// only the instruction pointed to by the program counter will be executed.
// If the contract execution yields any execution violation (i.e. out of gas,
// stack underflow, etc), the function returns statusFailed
func execute(c *context, oneStepOnly bool) status {
	status, error := steps(c, oneStepOnly)
	if error != nil {
		return statusFailed
	}
	return status
}

// steps executes the contract code in the given context,
// If oneStepOnly is true, only the instruction pointed to by the program
// counter will be executed.
// steps returns the status of the execution and an error if the contract
// execution yields any execution violation (i.e. out of gas, stack underflow, etc).
func steps(c *context, oneStepOnly bool) (status, error) {
	staticGasPrices := getStaticGasPrices(c.params.Revision)

	status := statusRunning
	for status == statusRunning {
		if int(c.pc) >= len(c.code) {
			return statusStopped, nil
		}

		op := vm.OpCode(c.code[c.pc])

		// Consume static gas price for instruction before execution
		if err := c.useGas(staticGasPrices.get(op)); err != nil {
			return status, err
		}

		var err error

		// Execute instruction
		switch op {
		case vm.POP:
			err = opPop(c)
		case vm.PUSH0:
			err = opPush0(c)
		case vm.PUSH1:
			err = opPush1(c)
		case vm.PUSH2:
			err = opPush2(c)
		case vm.PUSH3:
			err = opPush3(c)
		case vm.PUSH4:
			err = opPush4(c)
		case vm.PUSH5:
			err = opPush(c, 5)
		case vm.PUSH31:
			err = opPush(c, 31)
		case vm.PUSH32:
			err = opPush32(c)
		case vm.JUMP:
			err = opJump(c)
		case vm.JUMPDEST:
			// nothing
		case vm.SWAP1:
			err = opSwap(c, 1)
		case vm.SWAP2:
			err = opSwap(c, 2)
		case vm.DUP3:
			err = opDup(c, 3)
		case vm.AND:
			err = opAnd(c)
		case vm.SWAP3:
			err = opSwap(c, 3)
		case vm.JUMPI:
			err = opJumpi(c)
		case vm.GT:
			err = opGt(c)
		case vm.DUP4:
			err = opDup(c, 4)
		case vm.DUP2:
			err = opDup(c, 2)
		case vm.ISZERO:
			err = opIszero(c)
		case vm.ADD:
			err = opAdd(c)
		case vm.OR:
			err = opOr(c)
		case vm.XOR:
			err = opXor(c)
		case vm.NOT:
			err = opNot(c)
		case vm.SUB:
			err = opSub(c)
		case vm.MUL:
			err = opMul(c)
		case vm.MULMOD:
			err = opMulMod(c)
		case vm.DIV:
			err = opDiv(c)
		case vm.SDIV:
			err = opSDiv(c)
		case vm.MOD:
			err = opMod(c)
		case vm.SMOD:
			err = opSMod(c)
		case vm.ADDMOD:
			err = opAddMod(c)
		case vm.EXP:
			err = opExp(c)
		case vm.DUP5:
			err = opDup(c, 5)
		case vm.DUP1:
			err = opDup(c, 1)
		case vm.EQ:
			err = opEq(c)
		case vm.PC:
			err = opPc(c)
		case vm.CALLER:
			err = opCaller(c)
		case vm.CALLDATALOAD:
			err = opCallDataload(c)
		case vm.CALLDATASIZE:
			err = opCallDatasize(c)
		case vm.CALLDATACOPY:
			err = genericDataCopy(c, c.params.Input)
		case vm.MLOAD:
			err = opMload(c)
		case vm.MSTORE:
			err = opMstore(c)
		case vm.MSTORE8:
			err = opMstore8(c)
		case vm.MSIZE:
			err = opMsize(c)
		case vm.MCOPY:
			err = opMcopy(c)
		case vm.LT:
			err = opLt(c)
		case vm.SLT:
			err = opSlt(c)
		case vm.SGT:
			err = opSgt(c)
		case vm.SHR:
			err = opShr(c)
		case vm.SHL:
			err = opShl(c)
		case vm.SAR:
			err = opSar(c)
		case vm.CLZ:
			err = opClz(c)
		case vm.SIGNEXTEND:
			err = opSignExtend(c)
		case vm.BYTE:
			err = opByte(c)
		case vm.SHA3:
			err = opSha3(c)
		case vm.CALLVALUE:
			err = opCallvalue(c)
		case vm.PUSH6:
			err = opPush(c, 6)
		case vm.PUSH7:
			err = opPush(c, 7)
		case vm.PUSH8:
			err = opPush(c, 8)
		case vm.PUSH9:
			err = opPush(c, 9)
		case vm.PUSH10:
			err = opPush(c, 10)
		case vm.PUSH11:
			err = opPush(c, 11)
		case vm.PUSH12:
			err = opPush(c, 12)
		case vm.PUSH13:
			err = opPush(c, 13)
		case vm.PUSH14:
			err = opPush(c, 14)
		case vm.PUSH15:
			err = opPush(c, 15)
		case vm.PUSH16:
			err = opPush(c, 16)
		case vm.PUSH17:
			err = opPush(c, 17)
		case vm.PUSH18:
			err = opPush(c, 18)
		case vm.PUSH19:
			err = opPush(c, 19)
		case vm.PUSH20:
			err = opPush(c, 20)
		case vm.PUSH21:
			err = opPush(c, 21)
		case vm.PUSH22:
			err = opPush(c, 22)
		case vm.PUSH23:
			err = opPush(c, 23)
		case vm.PUSH24:
			err = opPush(c, 24)
		case vm.PUSH25:
			err = opPush(c, 25)
		case vm.PUSH26:
			err = opPush(c, 26)
		case vm.PUSH27:
			err = opPush(c, 27)
		case vm.PUSH28:
			err = opPush(c, 28)
		case vm.PUSH29:
			err = opPush(c, 29)
		case vm.PUSH30:
			err = opPush(c, 30)
		case vm.SWAP4:
			err = opSwap(c, 4)
		case vm.SWAP5:
			err = opSwap(c, 5)
		case vm.SWAP6:
			err = opSwap(c, 6)
		case vm.SWAP7:
			err = opSwap(c, 7)
		case vm.SWAP8:
			err = opSwap(c, 8)
		case vm.SWAP9:
			err = opSwap(c, 9)
		case vm.SWAP10:
			err = opSwap(c, 10)
		case vm.SWAP11:
			err = opSwap(c, 11)
		case vm.SWAP12:
			err = opSwap(c, 12)
		case vm.SWAP13:
			err = opSwap(c, 13)
		case vm.SWAP14:
			err = opSwap(c, 14)
		case vm.SWAP15:
			err = opSwap(c, 15)
		case vm.SWAP16:
			err = opSwap(c, 16)
		case vm.DUP6:
			err = opDup(c, 6)
		case vm.DUP7:
			err = opDup(c, 7)
		case vm.DUP8:
			err = opDup(c, 8)
		case vm.DUP9:
			err = opDup(c, 9)
		case vm.DUP10:
			err = opDup(c, 10)
		case vm.DUP11:
			err = opDup(c, 11)
		case vm.DUP12:
			err = opDup(c, 12)
		case vm.DUP13:
			err = opDup(c, 13)
		case vm.DUP14:
			err = opDup(c, 14)
		case vm.DUP15:
			err = opDup(c, 15)
		case vm.DUP16:
			err = opDup(c, 16)
		case vm.RETURN:
			err = opEndWithResult(c)
			status = statusReturned
		case vm.REVERT:
			status = statusReverted
			err = opEndWithResult(c)
		case vm.SLOAD:
			err = opSload(c)
		case vm.SSTORE:
			err = opSstore(c)
		case vm.TLOAD:
			err = opTload(c)
		case vm.TSTORE:
			err = opTstore(c)
		case vm.CODESIZE:
			err = opCodeSize(c)
		case vm.CODECOPY:
			err = genericDataCopy(c, c.params.Code)
		case vm.EXTCODESIZE:
			err = opExtcodesize(c)
		case vm.EXTCODEHASH:
			err = opExtcodehash(c)
		case vm.EXTCODECOPY:
			err = opExtCodeCopy(c)
		case vm.BALANCE:
			err = opBalance(c)
		case vm.SELFBALANCE:
			err = opSelfbalance(c)
		case vm.BASEFEE:
			err = opBaseFee(c)
		case vm.BLOBHASH:
			err = opBlobHash(c)
		case vm.BLOBBASEFEE:
			err = opBlobBaseFee(c)
		case vm.SELFDESTRUCT:
			status, err = opSelfdestruct(c)
		case vm.CHAINID:
			err = opChainId(c)
		case vm.GAS:
			err = opGas(c)
		case vm.PREVRANDAO:
			err = opPrevRandao(c)
		case vm.TIMESTAMP:
			err = opTimestamp(c)
		case vm.NUMBER:
			err = opNumber(c)
		case vm.GASLIMIT:
			err = opGasLimit(c)
		case vm.GASPRICE:
			err = opGasPrice(c)
		case vm.CALL:
			err = opCall(c)
		case vm.CALLCODE:
			err = opCallCode(c)
		case vm.STATICCALL:
			err = opStaticCall(c)
		case vm.DELEGATECALL:
			err = opDelegateCall(c)
		case vm.RETURNDATASIZE:
			err = opReturnDataSize(c)
		case vm.RETURNDATACOPY:
			err = opReturnDataCopy(c)
		case vm.BLOCKHASH:
			err = opBlockhash(c)
		case vm.COINBASE:
			err = opCoinbase(c)
		case vm.ORIGIN:
			err = opOrigin(c)
		case vm.ADDRESS:
			err = opAddress(c)
		case vm.STOP:
			status = opStop()
		case vm.CREATE:
			err = genericCreate(c, tosca.Create)
		case vm.CREATE2:
			err = genericCreate(c, tosca.Create2)
		case vm.LOG0:
			err = opLog(c, 0)
		case vm.LOG1:
			err = opLog(c, 1)
		case vm.LOG2:
			err = opLog(c, 2)
		case vm.LOG3:
			err = opLog(c, 3)
		case vm.LOG4:
			err = opLog(c, 4)
		default:
			err = errInvalidOpCode
		}

		if err != nil {
			return status, err
		}

		c.pc++

		if oneStepOnly {
			return status, nil
		}
	}
	return status, nil
}
