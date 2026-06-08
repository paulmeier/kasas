package csv

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/source"
)

func boolPtr(b bool) *bool { return &b }

func TestParseCSV_SignedAmountHeaderAutodetect(t *testing.T) {
	in := "Date,Description,Amount\n2024-01-15,Coffee,-4.50\n2024-01-16,Paycheck,2000.00\n"
	txns, skipped, err := parseCSV(strings.NewReader(in), Folder{Name: "acct"})
	require.NoError(t, err)
	assert.Zero(t, skipped)
	require.Len(t, txns, 2)

	assert.Equal(t, "-4.50", txns[0].Amount)
	assert.Equal(t, "Coffee", txns[0].Description)
	want, _ := time.Parse("2006-01-02", "2024-01-15")
	assert.Equal(t, want.Unix(), txns[0].Date)
	assert.True(t, strings.HasPrefix(txns[0].ExternalID, "csv:"))
	assert.NotEqual(t, txns[0].ExternalID, txns[1].ExternalID)
}

func TestParseCSV_DebitCreditPair(t *testing.T) {
	in := "Transaction Date,Details,Debit,Credit\n01/20/2024,ATM,40.00,\n01/21/2024,Refund,,15.00\n"
	txns, skipped, err := parseCSV(strings.NewReader(in), Folder{Name: "a"})
	require.NoError(t, err)
	assert.Zero(t, skipped)
	require.Len(t, txns, 2)
	assert.Equal(t, "-40", txns[0].Amount) // debit is an outflow
	assert.Equal(t, "15", txns[1].Amount)  // credit is an inflow
}

func TestParseCSV_NoHeaderByIndex(t *testing.T) {
	in := "2024-02-01,Foo,-1.00\n"
	f := Folder{Name: "a", Mapping: Mapping{
		HasHeader:         boolPtr(false),
		DateColumn:        "0",
		DescriptionColumn: "1",
		AmountColumn:      "2",
	}}
	txns, _, err := parseCSV(strings.NewReader(in), f)
	require.NoError(t, err)
	require.Len(t, txns, 1)
	assert.Equal(t, "-1.00", txns[0].Amount)
	assert.Equal(t, "Foo", txns[0].Description)
}

func TestParseCSV_ExplicitDateFormatAndDelimiter(t *testing.T) {
	in := "Date;Amount\n15.01.2024;1.00\n"
	f := Folder{Name: "a", Mapping: Mapping{Delimiter: ";", DateFormat: "02.01.2006"}}
	txns, skipped, err := parseCSV(strings.NewReader(in), f)
	require.NoError(t, err)
	assert.Zero(t, skipped)
	require.Len(t, txns, 1)
	want, _ := time.Parse("02.01.2006", "15.01.2024")
	assert.Equal(t, want.Unix(), txns[0].Date)
}

func TestParseCSV_SkipsUnparseableAndBlankRows(t *testing.T) {
	in := "Date,Amount\n2024-01-01,1.00\nnotadate,2.00\n\n2024-01-03,\n"
	txns, skipped, err := parseCSV(strings.NewReader(in), Folder{Name: "a"})
	require.NoError(t, err)
	require.Len(t, txns, 1)     // only the first data row maps
	assert.Equal(t, 2, skipped) // bad date + empty amount (the blank line is not counted)
}

func TestParseCSV_MissingDateColumnErrors(t *testing.T) {
	in := "Foo,Bar\n1,2\n"
	_, _, err := parseCSV(strings.NewReader(in), Folder{Name: "a"})
	require.Error(t, err)
}

func TestParseCSV_NamedColumnNotFoundErrors(t *testing.T) {
	in := "Date,Amount\n2024-01-01,1.00\n"
	f := Folder{Name: "a", Mapping: Mapping{DateColumn: "When"}}
	_, _, err := parseCSV(strings.NewReader(in), f)
	require.Error(t, err)
}

func TestCleanAmount(t *testing.T) {
	ok := map[string]string{
		"-4.50":     "-4.50",
		"$1,234.56": "1234.56",
		"(12.00)":   "-12.00",
		"+5":        "5",
		"  20.00  ": "20.00",
	}
	for in, want := range ok {
		got, valid := cleanAmount(in)
		require.True(t, valid, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "abc", "  ", "$"} {
		_, valid := cleanAmount(bad)
		assert.False(t, valid, bad)
	}
}

func TestContentHashStableAndProfileScoped(t *testing.T) {
	txn := source.ImportTxn{Amount: "-4.50", Date: 100, Description: "Coffee"}
	h1 := contentHash("acctA", txn)
	assert.Equal(t, h1, contentHash("acctA", txn), "deterministic")
	assert.NotEqual(t, h1, contentHash("acctB", txn), "profile-scoped")
	assert.True(t, strings.HasPrefix(h1, "csv:"))
}
