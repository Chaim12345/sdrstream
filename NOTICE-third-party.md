# Third-party notices

go/internal/decode/ft8.go and ftx_tables.go are a Go port of the decode path
of kgoba/ft8_lib (MIT License, Copyright (c) 2018 Karlis Goba); the LDPC
tables come from WSJT-X via ft8_lib. License text:
go/internal/decode/testdata/ft8_lib.LICENSE. Test recordings under
go/internal/decode/testdata/ft8 come from the ft8_lib test suite.

go/internal/decode/wspr.go and wspr_tables.go are a Go port of the single-pass
path of wsprd from WSJT-X (GNU GPL v3; Copyright 2001-2018 Joe Taylor K1JT,
2014-2018 Steven Franke K9AN; Fano decoder Copyright 1994 Phil Karn KA9Q),
read from github.com/pavel-demin/wsprd. These files are GPL v3, so a binary
that includes them is GPL v3. Reference oracle for tests: wsprd built from
the same source.
