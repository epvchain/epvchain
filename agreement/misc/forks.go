
package misc

import (
	"fmt"

	"github.com/epvchain/go-epvchain/public"
	"github.com/epvchain/go-epvchain/kernel/types"
	"github.com/epvchain/go-epvchain/content"
)

func VerifyForkHashes(config *params.ChainConfig, header *types.Header, uncle bool) error {

	if uncle {
		return nil
	}

	if config.EIP150Block != nil && config.EIP150Block.Cmp(header.Number) == 0 {
		if config.EIP150Hash != (common.Hash{}) && config.EIP150Hash != header.Hash() {
			return fmt.Errorf("homestead gas reprice fork: have 0x%x, want 0x%x", header.Hash(), config.EIP150Hash)
		}
	}

	return nil
}
