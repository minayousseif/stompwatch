// Package settings holds the settings the collector is running with and
// applies the changes the dashboard makes.
//
// The config file holds the defaults. Rows in the database config table
// override a few of them, and the running process picks a change up without
// a restart.
//
// Apply never writes to the database. The HTTP layer calls Apply first and
// writes the rows with store.PutSettings only after Apply returns nil, so a
// rejected change never reaches the disk.
package settings
