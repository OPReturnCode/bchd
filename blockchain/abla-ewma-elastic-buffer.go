package blockchain

import (
	"fmt"
	"os"
	"strings"

	"lukechampine.com/uint128"
)

var (
	// Post-filter set to 2 GB due to limitation of 32-bit architectures
	// block storage.
	// When 32-bit will be deprecated, it should be entirely removed.
	TEMP_32_BIT_MAX_SAFE_BLOCKSIZE_LIMIT = uint64(2000000000)
)

const (
	UINT64_MAX = ^uint64(0)

	// Constant 2^7, used as fixed precision for algorithm's "asymmetry
	// factor" configuration value, e.g. we will store the real number 1.5
	// as integer 192 so when we want to multiply or divide an integer with
	// value of 1.5, we will do muldiv(value, 192, B7) or
	// muldiv(value, B7, 192).
	B7 = uint64(1) << 7

	// Sanity ranges for configuration values
	MIN_ZETA_XB7         = uint64(129) // zeta real value of 1.0078125
	MAX_ZETA_XB7         = uint64(256) // zeta real value of 2.0000000
	MIN_GAMMA_RECIPROCAL = uint64(9484)
	MAX_GAMMA_RECIPROCAL = uint64(151744)
	MIN_DELTA            = uint64(0)
	MAX_DELTA            = uint64(32)
	MIN_THETA_RECIPROCAL = uint64(9484)
	MAX_THETA_RECIPROCAL = uint64(151744)
)

// Utility function to calculate x * y / z where intermediate product
// can overflow uint64 but the final result can not.
func muldiv(x, y, z uint64) uint64 {
	if z == 0 {
		fmt.Fprintf(os.Stderr, "muldiv divide by 0\n")
		os.Exit(1)
	}
	bx := uint128.From64(x)
	by := uint128.From64(y)
	bz := uint128.From64(z)
	mul := bx.Mul(by)
	q := mul.Div(bz)
	if q.Hi > 0 {
		fmt.Fprintf(os.Stderr, "muldiv result overflow\n")
		os.Exit(1)
	}
	return q.Lo
}

// Utility function
func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	} else {
		return b
	}
}

// Utility function
func maxUint64(a, b uint64) uint64 {
	if a > b {
		return a
	} else {
		return b
	}
}

// Algorithm configuration
type ABLAConfig struct {
	// Initial control block size value, also used as floor value
	epsilon0 uint64
	// Initial elastic buffer size value, also used as floor value
	beta0 uint64
	// Last block height which will have the initial block size limit
	n0 uint64
	// Reciprocal of control function "forget factor" value
	gammaReciprocal uint64
	// Control function "asymmetry factor" value
	zeta_xB7 uint64
	// Reciprocal of elastic buffer decay rate
	thetaReciprocal uint64
	// Elastic buffer "gear factor"
	delta uint64
	// Maximum control block size value
	epsilonMax uint64
	// Maximum elastic buffer size value
	betaMax uint64
}

// Set epsilonMax and betaMax such that algo's internal arithmetic ops can't overflow UINT64_MAX
func (config *ABLAConfig) SetMax() {
	maxSafeBlocksizeLimit := UINT64_MAX / config.zeta_xB7 * B7

	// elastic_buffer_ratio_max = (delta * gamma / theta * (zeta - 1)) / (gamma / theta * (zeta - 1) + 1)
	maxElasticBufferRatioNumerator := config.delta * ((config.zeta_xB7 - B7) * config.thetaReciprocal / config.gammaReciprocal)
	maxElasticBufferRatioDenominator := (config.zeta_xB7-B7)*config.thetaReciprocal/config.gammaReciprocal + B7

	config.epsilonMax = maxSafeBlocksizeLimit / (maxElasticBufferRatioNumerator + maxElasticBufferRatioDenominator) * maxElasticBufferRatioDenominator
	config.betaMax = maxSafeBlocksizeLimit - config.epsilonMax

	fmt.Fprintf(os.Stderr, "[INFO] Auto-configured epsilonMax: %d, betaMax: %d\n", config.epsilonMax, config.betaMax)
}

func (config *ABLAConfig) IsValid() (errs *strings.Builder) {
	if config.epsilon0 > config.epsilonMax {
		errs = new(strings.Builder)
		errs.WriteString("Error, initial control block size limit sanity check failed (epsilonMax)")
		return errs
	}
	if config.beta0 > config.betaMax {
		errs = new(strings.Builder)
		errs.WriteString("Error, initial elastic buffer size sanity check failed (betaMax).")
		return errs
	}
	if config.zeta_xB7 < MIN_ZETA_XB7 || config.zeta_xB7 > MAX_ZETA_XB7 {
		errs = new(strings.Builder)
		errs.WriteString("Error, zeta sanity check failed.")
		return errs
	}
	if config.gammaReciprocal < MIN_GAMMA_RECIPROCAL || config.gammaReciprocal > MAX_GAMMA_RECIPROCAL {
		errs = new(strings.Builder)
		errs.WriteString("Error, gammaReciprocal sanity check failed.")
		return errs
	}
	if config.delta+1 <= MIN_DELTA || config.delta > MAX_DELTA {
		errs = new(strings.Builder)
		errs.WriteString("Error, delta sanity check failed.")
		return errs
	}
	if config.thetaReciprocal < MIN_THETA_RECIPROCAL || config.thetaReciprocal > MAX_THETA_RECIPROCAL {
		errs = new(strings.Builder)
		errs.WriteString("Error, thetaReciprocal sanity check failed.")
		return errs
	}
	if config.epsilon0 < muldiv(config.gammaReciprocal, B7, config.zeta_xB7-B7) {
		// Required due to truncation of integer ops.
		// With this we ensure that the control size can be adjusted for at least 1 byte.
		// Also, with this we ensure that divisior bytesMax in calculateNextABLAState() can't be 0.
		errs = new(strings.Builder)
		errs.WriteString("Error, epsilon0 sanity check failed. Too low relative to gamma and zeta.")
		return errs
	}
	return nil
}

// Algorithm's internal state
// Note: limit for the block with blockHeight will be given by
// controlBlockSize + elasticBufferSize
type ABLAState struct {
	// Block height for which the state applies
	blockHeight uint64
	// Control function state
	controlBlockSize uint64
	// Elastic buffer function state
	elasticBufferSize uint64
}

// Returns true if this state is valid relative to `config`. On false return, optional out `errs` is set
// to point to a constant string explaining the reason that this state is invalid.
func (state *ABLAState) IsValid(config *ABLAConfig) (errs *strings.Builder) {
	if state.controlBlockSize < config.epsilon0 || state.controlBlockSize > config.epsilonMax {
		errs = new(strings.Builder)
		errs.WriteString("Error, invalid controlBlockSize state. Can't be below initialization value or above epsilonMax.")
		return errs
	}
	if state.elasticBufferSize < config.beta0 || state.elasticBufferSize > config.betaMax {
		errs = new(strings.Builder)
		errs.WriteString("Error, invalid elasticBufferSize state. Can't be below initialization value or above betaMax.")
		return errs
	}
	return nil
}

// Calculate the limit for the block to which the algorithm's state
// applies, given algorithm state
func (state *ABLAState) getBlockSizeLimit() uint64 {
	return minUint64(state.controlBlockSize+state.elasticBufferSize, TEMP_32_BIT_MAX_SAFE_BLOCKSIZE_LIMIT)
	// Note: Remove the TEMP_32_BIT_MAX_SAFE_BLOCKSIZE_LIMIT limit once 32-bit architecture is deprecated:
	// return state.controlBlockSize + state.elasticBufferSize
}

// Calculate algorithm's look-ahead block size limit, for a block N blocks ahead of current one.
// Returns the limit for block with current+N height, assuming all blocks 100% full.
func (state *ABLAState) lookaheadState(config *ABLAConfig, count uint) ABLAState {
	lookaheadState := *state
	for i := uint(0); i < count; i++ {
		maxSize := lookaheadState.getBlockSizeLimit()
		lookaheadState = lookaheadState.nextABLAState(config, maxSize)
	}
	return lookaheadState
}

// Calculate algorithm's state for the next block (n), given
// current blockchain tip (n-1) block size, algorithm state, and
// algorithm configuration. Returns the next state after this block.
func (state *ABLAState) nextABLAState(config *ABLAConfig, currentBlockSize uint64) ABLAState {
	// Next block's ABLA state
	var newState ABLAState

	// n = n + 1
	newState.blockHeight = state.blockHeight + 1

	// For safety: we clamp this current block's blocksize to the maximum value this algorithm expects. Normally this
	// won't happen unless the node is run with some -excessiveblocksize parameter that permits larger blocks than this
	// algo's current state expects.
	currentBlockSize = minUint64(currentBlockSize, state.controlBlockSize+state.elasticBufferSize)

	// if block height is in range 0 to n0 inclusive use initialization values
	// else use algorithmic limit
	if newState.blockHeight <= config.n0 {
		// epsilon_n = epsilon_0
		newState.controlBlockSize = config.epsilon0
		// beta_n = beta_0
		newState.elasticBufferSize = config.beta0
	} else {
		// control function

		// zeta * x_{n-1}
		amplifiedCurrentBlockSize := muldiv(config.zeta_xB7, currentBlockSize, B7)

		// if zeta * x_{n-1} > epsilon_{n-1} then increase
		// else decrease or no change
		if amplifiedCurrentBlockSize > state.controlBlockSize {
			// zeta * x_{n-1} - epsilon_{n-1}
			bytesToAdd := amplifiedCurrentBlockSize - state.controlBlockSize

			// zeta * y_{n-1}
			amplifiedBlockSizeLimit := muldiv(config.zeta_xB7, state.controlBlockSize+state.elasticBufferSize, B7)

			// zeta * y_{n-1} - epsilon_{n-1}
			bytesMax := amplifiedBlockSizeLimit - state.controlBlockSize

			// zeta * beta_{n-1} * (zeta * x_{n-1} - epsilon_{n-1}) / (zeta * y_{n-1} - epsilon_{n-1})
			scalingOffset := muldiv(muldiv(config.zeta_xB7, state.elasticBufferSize, B7),
				bytesToAdd, bytesMax)
			// epsilon_n = epsilon_{n-1} + gamma * (zeta * x_{n-1} - epsilon_{n-1} - zeta * beta_{n-1} * (zeta * x_{n-1} - epsilon_{n-1}) / (zeta * y_{n-1} - epsilon_{n-1}))
			newState.controlBlockSize = state.controlBlockSize + (bytesToAdd-scalingOffset)/config.gammaReciprocal
		} else {
			// epsilon_{n-1} - zeta * x_{n-1}
			bytesToRemove := state.controlBlockSize - amplifiedCurrentBlockSize

			// epsilon_{n-1} + gamma * (zeta * x_{n-1} - epsilon_{n-1})
			// rearranged to:
			// epsilon_{n-1} - gamma * (epsilon_{n-1} - zeta * x_{n-1})
			newState.controlBlockSize = state.controlBlockSize - bytesToRemove/config.gammaReciprocal

			// epsilon_n = max(epsilon_{n-1} + gamma * (zeta * x_{n-1} - epsilon_{n-1}), epsilon_0)
			newState.controlBlockSize = maxUint64(newState.controlBlockSize, config.epsilon0)
		}

		// elastic buffer function

		// beta_{n-1} * theta
		bufferDecay := state.elasticBufferSize / config.thetaReciprocal

		// if zeta * x_{n-1} > epsilon_{n-1} then increase
		// else decrease or no change
		if amplifiedCurrentBlockSize > state.controlBlockSize {
			// (epsilon_{n} - epsilon_{n-1}) * delta
			bytesToAdd := (newState.controlBlockSize - state.controlBlockSize) * config.delta

			// beta_{n-1} - beta_{n-1} * theta + (epsilon_{n} - epsilon_{n-1}) * delta
			newState.elasticBufferSize = state.elasticBufferSize - bufferDecay + bytesToAdd
		} else {
			// beta_{n-1} - beta_{n-1} * theta
			newState.elasticBufferSize = state.elasticBufferSize - bufferDecay
		}
		// max(beta_{n-1} - beta_{n-1} * theta + (epsilon_{n} - epsilon_{n-1}) * delta, beta_0) , if zeta * x_{n-1} > epsilon_{n-1}
		// max(beta_{n-1} - beta_{n-1} * theta, beta_0) , if zeta * x_{n-1} <= epsilon_{n-1}
		newState.elasticBufferSize = maxUint64(newState.elasticBufferSize, config.beta0)

		// clip controlBlockSize to epsilonMax to avoid integer overflow for extreme sizes
		newState.controlBlockSize = minUint64(newState.controlBlockSize, config.epsilonMax)
		// clip elasticBufferSize to betaMax to avoid integer overflow for extreme sizes
		newState.elasticBufferSize = minUint64(newState.elasticBufferSize, config.betaMax)
	}
	return newState
}

func main() {
	var configInitialExcessiveBlockSize uint64 = 32000000 // excessiveblocksize

	//ablaconfig
	config := &ABLAConfig{
		beta0:           0,
		n0:              0,
		gammaReciprocal: 0,
		zeta_xB7:        0,
		thetaReciprocal: 0,
		delta:           0,
	}

	// Finalize config
	if configInitialExcessiveBlockSize > config.beta0 {
		config.epsilon0 = configInitialExcessiveBlockSize - config.beta0
		config.SetMax()
	} else {
		fmt.Fprintf(os.Stderr, "Error, initial block size limit relative to initial elastic buffer size sanity check failed.\n")
		os.Exit(1)
	}

	// Sanity check the config
	errs := config.IsValid()
	if errs != nil {
		fmt.Fprintf(os.Stderr, "ABLA Config sanity check failed: %s\n", errs.String())
		os.Exit(1)
	}

	// Initialize state for height 0
	state := ABLAState{
		blockHeight:       0,
		controlBlockSize:  config.epsilon0,
		elasticBufferSize: config.beta0,
	}

	errs = state.IsValid(config)
	if errs != nil {
		fmt.Fprintf(os.Stderr, "ABLA state sanity check failed: %s\n", errs.String())
		os.Exit(1)
	}

	// Calculate and print
	var blockSize uint64
	blockSize = minUint64(blockSize, state.getBlockSizeLimit())
	// calculate the next block's algorithm state
	state = state.nextABLAState(config, blockSize) // TODO UPDATE STATE AFTER BLOCK ACCEPTED

	blockSizeLimit := state.getBlockSizeLimit() // TODO CHECKS BLOCK SIZE LIMIT

	fmt.Printf("%d,%d,%d,%d,%d\n",
		state.blockHeight-1, blockSize, blockSizeLimit, state.elasticBufferSize,
		state.controlBlockSize)

}
