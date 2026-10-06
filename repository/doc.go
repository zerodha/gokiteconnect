// Package repository provides storage for the Kite instrument catalog.
//
// NewInstrumentsDuckDBRepository opens a persistent kite.InstrumentRepository. Its database
// path must be new or point to a catalog previously created by NewInstrumentsDuckDBRepository.
// Attach it with kite.Client.SetInstrumentRepository.
package repository
