package main

import (
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
)

func productionVariant(variant string)string{require(variant=="P","pristine binary supports P only");return "upstream-geth-v1.17.8"}
func configureVariant(_ *ethconfig.Config,variant string){productionVariant(variant)}
func assertVariant(_ *eth.Ethereum,variant string){productionVariant(variant)}
