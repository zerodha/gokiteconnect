// Package repository provides storage for the Kite instrument catalog.
//
// NewInstrumentsDuckDBRepository opens a persistent kiteconnect.InstrumentRepository. Its database
// path must be new or point to a catalog previously created by NewInstrumentsDuckDBRepository.
// Attach it with kiteconnect.Client.SetInstrumentRepository.
package repository
