package formatos

import (
	"fmt"
	"strings"
	"time"
)

// ServiceItem is one line of the service order.
type ServiceItem struct{ Description, Unit, Quantity string }

// ServiceOrder is the "Solicitud y autorización del servicio".
type ServiceOrder struct {
	Date      time.Time
	Km        *int
	Partida   string
	Recurso   string
	Execution string
	Bus       Bus
	Items     []ServiceItem
}

const ordenSheet = "Hoja1"

// The printed form has four lines of items (rows 18 to 21).
const ordenItems = 4

// Orden fills the service order form from a maintenance order, plus the panel's overrides.
func Orden(o ServiceOrder, overrides Values) (*Book, error) {
	b, err := open(ordenXLSX)
	if err != nil {
		return nil, err
	}
	if err := b.fillOrden(o); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.apply(overrides); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *Book) fillOrden(o ServiceOrder) error {
	sh := ordenSheet
	km := ""
	if o.Km != nil {
		km = fmt.Sprint(*o.Km)
	}
	mark := ""
	if o.Execution != "" {
		mark = "X"
	}
	for _, f := range []struct {
		cell string
		v    any
	}{
		{"F11", km}, {"F12", o.Date.Format("02/01/2006")}, {"F13", o.Partida}, {"F14", o.Recurso}, {"B15", o.Bus.Label()},
		{"F15", strings.ToUpper(o.Execution)}, {"F16", mark},
		// What the form already says, editable too: área, destino, proyecto and the signatures.
		{"B12", nil}, {"B13", nil}, {"B14", nil}, {"C23", nil}, {"C24", nil}, {"C25", nil}, {"C26", nil},
	} {
		if err := b.field(sh, f.cell, f.v); err != nil {
			return err
		}
	}
	if err := centerCell(b.f, sh, "F16"); err != nil {
		return err
	}
	for i := 0; i < ordenItems; i++ {
		row := 18 + i
		var it ServiceItem
		no := ""
		if i < len(o.Items) {
			it, no = o.Items[i], fmt.Sprint(i+1)
		}
		for _, f := range []struct {
			col string
			v   string
		}{{"A", no}, {"B", strings.ToUpper(it.Description)}, {"E", strings.ToUpper(it.Unit)}, {"F", it.Quantity}} {
			if err := b.field(sh, fmt.Sprintf("%s%d", f.col, row), f.v); err != nil {
				return err
			}
		}
	}
	return nil
}

// VehicleReport is the "Reporte de vehículo".
type VehicleReport struct {
	Date         time.Time
	Bus          Bus
	Observations string
}

const vehiculoSheet = "Hoja4"

// observation lines on the form (rows 8 to 15) and roughly how many characters fit in each.
const (
	obsFirstRow = 8
	obsRows     = 8
	obsWidth    = 110
)

// Vehiculo fills the vehicle report form (the Hoja4 sheet of the log workbook), plus the panel's overrides.
func Vehiculo(v VehicleReport, overrides Values) (*Book, error) {
	b, err := open(bitacorasXLSX)
	if err != nil {
		return nil, err
	}
	if err := b.fillVehiculo(v); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.apply(overrides); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *Book) fillVehiculo(v VehicleReport) error {
	if err := b.keepOnly(vehiculoSheet); err != nil {
		return err
	}
	sh := vehiculoSheet
	model := strings.TrimSpace(v.Bus.Brand + " " + v.Bus.Model)
	for cell, val := range map[string]string{
		"A2": "FECHA: " + LongDate(v.Date),
		"A3": "No. Economico: " + v.Bus.Number,
		"A4": "Placas: " + v.Bus.Plates,
		"A5": "Modelo: " + model,
	} {
		if err := b.field(sh, cell, val); err != nil {
			return err
		}
	}
	lines := wrap(strings.ToUpper(v.Observations), obsWidth)
	if len(lines) > obsRows {
		// Keep everything: the rest goes on the last line, wrapped inside the cell.
		lines = append(lines[:obsRows-1], strings.Join(lines[obsRows-1:], " "))
	}
	for i := 0; i < obsRows; i++ {
		var text any // an unused line keeps the form's "_____"
		if i < len(lines) {
			text = lines[i]
		}
		if err := b.field(sh, fmt.Sprintf("A%d", obsFirstRow+i), text); err != nil {
			return err
		}
	}
	return b.fillable(sh, "A17:A18")
}
