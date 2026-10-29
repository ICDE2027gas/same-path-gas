package main

import (
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
)

func productionVariant(variant string)string{require(variant=="S"||variant=="C","shared binary supports S/C only");if variant=="C"{return "research-certificate"};return "research-modified-stock"}
func configureVariant(config *ethconfig.Config,variant string){productionVariant(variant);config.EstimateGasCertificate=variant=="C"}
func assertVariant(backend *eth.Ethereum,variant string){require(backend.APIBackend.EstimateGasCertificateEnabled()==(variant=="C"),"immutable backend choice")}
