package main

import (
	_ "embed"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

// C# bindings generator.
//
// Generates a raw, 1:1 unsafe C# binding of the C API described by the yaml:
// the same type, function and field names as webgpu.h, blittable structs,
// typed handle structs, enums, and [DllImport] declarations. Idiomatic wrappers
// are expected to be layered on top in a separate project.

//go:embed csharp.tmpl
var csharpTmpl string

// CSField is one field declaration of a generated C# struct.
type CSField struct {
	Doc  string
	Decl string
}

func (g *Generator) GenCSharp(dst io.Writer) error {
	t := template.
		New("").
		Funcs(template.FuncMap{
			"CSComment":           func(v string) string { return Comment(v, CommentTypeSingleLine, 0, false) },
			"CSDoc":               CSDoc,
			"CSFunctionDoc":       CSFunctionDoc,
			"CSIdent":             CSIdent,
			"CSType":              g.CSType,
			"CSHandleType":        func(o Object) string { return g.CType(o.Base, "") },
			"CSConstant":          g.CSConstant,
			"CSEnumValueName":     g.CSEnumValueName,
			"CEnumValueName":      g.CEnumValueName,
			"CMethodName":         g.CMethodName,
			"PascalCaseName":      g.PascalCaseName,
			"ConstantCaseName":    g.ConstantCaseName,
			"EnumValue32":         g.EnumValue32,
			"BitflagValue":        func(b Bitflag, entryIndex int) (string, error) { return g.BitflagValue(b, entryIndex, false) },
			"CSCallbackPointer":   g.CSCallbackPointer,
			"CSStructFields":      g.CSStructFields,
			"CSStructInit":        g.CSStructInit,
			"CSFunctionReturns":   g.CSFunctionReturns,
			"CSFunctionArgs":      g.CSFunctionArgs,
			"CSFunctionArgsNoObj": func(f Function) string { return g.CSFunctionArgs(f, nil) },
		})
	t, err := t.Parse(csharpTmpl)
	if err != nil {
		return fmt.Errorf("GenCSharp: failed to parse template: %w", err)
	}
	if err := t.Execute(dst, g); err != nil {
		return fmt.Errorf("GenCSharp: failed to execute template: %w", err)
	}
	return nil
}

var csharpKeywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`abstract as base bool break byte case catch char checked class const
		continue decimal default delegate do double else enum event explicit extern false finally fixed
		float for foreach goto if implicit in int interface internal is lock long namespace new null object
		operator out override params private protected public readonly ref return sbyte sealed short sizeof
		stackalloc static string struct switch this throw true try typeof uint ulong unchecked unsafe ushort
		using virtual void volatile while`) {
		csharpKeywords[k] = true
	}
}

// CSIdent escapes C# keywords and identifiers that start with a digit.
func CSIdent(s string) string {
	if csharpKeywords[s] {
		return "@" + s
	}
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		return "_" + s
	}
	return s
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func docLines(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || v == "TODO" {
		return nil
	}
	return strings.Split(xmlEscaper.Replace(v), "\n")
}

func writeDocTag(sb *strings.Builder, indent string, open string, close string, lines []string) {
	fmt.Fprintf(sb, "%s/// %s\n", indent, open)
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			fmt.Fprintf(sb, "%s///\n", indent)
		} else {
			fmt.Fprintf(sb, "%s/// %s\n", indent, l)
		}
	}
	fmt.Fprintf(sb, "%s/// %s\n", indent, close)
}

// CSDoc renders a doc string as an XML <summary> comment (with a leading
// newline, so empty docs render as nothing).
func CSDoc(v string, indent int) string {
	lines := docLines(v)
	if lines == nil {
		return ""
	}
	sb := &strings.Builder{}
	sb.WriteString("\n")
	writeDocTag(sb, strings.Repeat(" ", indent), "<summary>", "</summary>", lines)
	return strings.TrimSuffix(sb.String(), "\n")
}

// CSFunctionDoc renders summary, params and returns of a function.
func CSFunctionDoc(fn *Function, indent int) string {
	ind := strings.Repeat(" ", indent)
	sb := &strings.Builder{}
	sb.WriteString("\n")
	if lines := docLines(fn.Doc); lines != nil {
		writeDocTag(sb, ind, "<summary>", "</summary>", lines)
	}
	for _, arg := range fn.Args {
		lines := docLines(arg.Doc)
		if arg.PassedWithOwnership != nil && *arg.PassedWithOwnership {
			lines = append(lines, "This parameter is passed with ownership.")
		}
		if lines != nil {
			writeDocTag(sb, ind, fmt.Sprintf(`<param name="%s">`, CSIdent(CamelCase(arg.Name))), "</param>", lines)
		}
	}
	if fn.Returns != nil {
		lines := docLines(fn.Returns.Doc)
		if fn.Returns.PassedWithOwnership != nil && *fn.Returns.PassedWithOwnership {
			lines = append(lines, "This value is returned with ownership.")
		}
		if lines != nil {
			writeDocTag(sb, ind, "<returns>", "</returns>", lines)
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// CSType maps a yaml type to its C# raw-binding type.
func (g *Generator) CSType(typ any, pointerType PointerType) string {
	var cstype string
	switch t := typ.(type) {
	case string:
		switch t {
		case "bool":
			cstype = "WGPUBool"
		case "nullable_string", "string_with_default_empty", "out_string":
			cstype = "WGPUStringView"
		case "uint16":
			cstype = "ushort"
		case "uint32":
			cstype = "uint"
		case "uint64":
			cstype = "ulong"
		case "usize":
			cstype = "nuint"
		case "int16":
			cstype = "short"
		case "int32":
			cstype = "int"
		case "float32", "nullable_float32":
			cstype = "float"
		case "float64", "float64_supertype":
			cstype = "double"
		case "c_void",
			"c_void_data_ptr",
			"c_void_mapped_range_ptr",
			"c_void_a_native_window",
			"c_void_ca_metal_layer",
			"c_void_h_instance",
			"c_void_h_wnd",
			"c_void_wl_display",
			"c_void_wl_surface",
			"c_void_x11_display",
			"c_void_xcb_connection":
			cstype = "void"
		default:
			// C# has no typedefs: resolve them to their underlying type.
			if name, ok := strings.CutPrefix(t, "typedef."); ok {
				idx := slices.IndexFunc(g.Typedefs, func(td Typedef) bool { return td.Name == name })
				if idx == -1 {
					panic("unknown typedef: " + t)
				}
				return g.CSType(g.Typedefs[idx].Type, pointerType)
			}
			return g.CSType(g.FindBaseType(t), pointerType)
		}
	case Base:
		cstype = "WGPU" + g.PascalCaseName(t)
	default:
		panic("Unknown input for type")
	}
	if pointerType != "" {
		cstype += "*"
	}
	return cstype
}

// CSEnumValueName is the C# member name of an enum or bitflag entry
// (the C name without the "WGPUType_" prefix).
func (g *Generator) CSEnumValueName(typ Base, entry Base) string {
	entryPrefix := g.GetNamespacePrefixForNestedItem(typ, entry)
	return CSIdent(PascalCase(g.CanonicalCaseName(entryPrefix, entry)))
}

// CSConstant returns the full C# declaration of a constant.
func (g *Generator) CSConstant(c Constant) (string, error) {
	name := "WGPU_" + g.ConstantCaseName(c.Base)
	switch c.Value {
	case "usize_max":
		// nuint.MaxValue is not a compile-time constant.
		return fmt.Sprintf("public static nuint %s => nuint.MaxValue;", name), nil
	case "uint32_max":
		return fmt.Sprintf("public const uint %s = uint.MaxValue;", name), nil
	case "uint64_max":
		return fmt.Sprintf("public const ulong %s = ulong.MaxValue;", name), nil
	case "nan":
		return fmt.Sprintf("public const float %s = float.NaN;", name), nil
	default:
		v, err := g.CValue(c.Value)
		if err != nil {
			return "", err
		}
		// CValue yields e.g. 0x10UL / 0x100000000ULL.
		if strings.HasSuffix(v, "ULL") {
			return fmt.Sprintf("public const ulong %s = %sUL;", name, strings.TrimSuffix(v, "ULL")), nil
		}
		return fmt.Sprintf("public const uint %s = %sU;", name, strings.TrimSuffix(v, "UL")), nil
	}
}

func (g *Generator) callbackArgTypes(c Callback) []string {
	var types []string
	for _, arg := range c.Args {
		matches := arrayTypeRegexp.FindStringSubmatch(arg.Type)
		if len(matches) == 2 {
			types = append(types, "nuint", g.CSType(matches[1], arg.Pointer))
		} else {
			types = append(types, g.CSType(arg.Type, arg.Pointer))
		}
	}
	return append(types, "void*", "void*", "void")
}

// CSCallbackPointer is the unmanaged function pointer type of a callback,
// matching the C signature exactly (by-value struct arguments stay by value).
func (g *Generator) CSCallbackPointer(c Callback) string {
	return "delegate* unmanaged<" + strings.Join(g.callbackArgTypes(c), ", ") + ">"
}

// CSStructFields returns the field declarations of a struct, including the
// chain header and expanded array (count + pointer) members.
func (g *Generator) CSStructFields(s Struct) []CSField {
	var fields []CSField
	switch s.Type {
	case "extensible", "extensible_callback_arg":
		fields = append(fields, CSField{Decl: "public WGPUChainedStruct* nextInChain;"})
	case "extension":
		fields = append(fields, CSField{Decl: "public WGPUChainedStruct chain;"})
	}
	for _, m := range s.Members {
		name := CSIdent(CamelCase(m.Name))
		doc := CSDoc(m.Doc, 4)
		matches := arrayTypeRegexp.FindStringSubmatch(m.Type)
		switch {
		case len(matches) == 2:
			fields = append(fields,
				CSField{
					Doc:  CSDoc("Array count for `"+CamelCase(m.Name)+"`.", 4),
					Decl: fmt.Sprintf("public nuint %s;", CSIdent(CamelCase(Singularize(m.Name))+"Count")),
				},
				CSField{Doc: doc, Decl: fmt.Sprintf("public %s %s;", g.CSType(matches[1], m.Pointer), name)},
			)
		case strings.HasPrefix(m.Type, "callback."):
			fields = append(fields, CSField{Doc: doc, Decl: fmt.Sprintf("public %sCallbackInfo %s;", g.CSType(m.Type, ""), name)})
		default:
			fields = append(fields, CSField{Doc: doc, Decl: fmt.Sprintf("public %s %s;", g.CSType(m.Type, m.Pointer), name)})
		}
	}
	return fields
}

func (g *Generator) enumEntryValue(enumName, entryName string) (uint32, bool) {
	idx := slices.IndexFunc(g.Enums, func(e Enum) bool { return e.Name == enumName })
	if idx == -1 {
		return 0, false
	}
	e := g.Enums[idx]
	for i, entry := range e.Entries {
		if entry != nil && entry.Name == entryName {
			v, err := g.EnumValue32(e, i)
			return v, err == nil
		}
	}
	return 0, false
}

func (g *Generator) bitflagEntryValueByName(bitflagName, entryName string) (uint64, bool) {
	idx := slices.IndexFunc(g.Bitflags, func(b Bitflag) bool { return b.Name == bitflagName })
	if idx == -1 {
		return 0, false
	}
	b := g.Bitflags[idx]
	for i, entry := range b.Entries {
		if entry.Name == entryName {
			if len(entry.ValueCombination) > 0 {
				return 1, true // non-zero combination
			}
			v, err := bitflagEntryValue(entry, i)
			return v, err == nil
		}
	}
	return 0, false
}

var numericLiteralRegexp = regexp.MustCompile(`^(0x[0-9a-fA-F]+|[0-9]+)$`)

// csDefaultValue returns the C# expression for a member's default value as
// set by the C INIT macro, or "" if the default is the zero value.
func (g *Generator) csDefaultValue(m ParameterType) string {
	constRef := func(def string) string {
		return "WGPU.WGPU_" + g.ConstantCaseName(g.FindBaseType(def))
	}
	switch {
	case m.Pointer != "":
		return ""
	case strings.HasPrefix(m.Type, "enum."):
		_, enumName, _ := strings.Cut(m.Type, ".")
		entry := "undefined"
		if m.Default != nil {
			entry = *m.Default
		}
		if v, ok := g.enumEntryValue(enumName, entry); !ok || v == 0 {
			return ""
		}
		return g.CSType(m.Type, "") + "." + CSIdent(PascalCase(entry))
	case strings.HasPrefix(m.Type, "bitflag."):
		if m.Default == nil {
			return ""
		}
		_, name, _ := strings.Cut(m.Type, ".")
		if v, ok := g.bitflagEntryValueByName(name, *m.Default); !ok || v == 0 {
			return ""
		}
		return g.CSType(m.Type, "") + "." + CSIdent(PascalCase(*m.Default))
	case m.Type == "uint16", m.Type == "uint32", m.Type == "uint64", m.Type == "usize", m.Type == "int32":
		switch {
		case m.Default == nil, *m.Default == "0":
			return ""
		case strings.HasPrefix(*m.Default, "constant."):
			return constRef(*m.Default)
		case numericLiteralRegexp.MatchString(*m.Default):
			return *m.Default
		default:
			panic("unknown integer default: " + *m.Default)
		}
	case m.Type == "float32" || m.Type == "nullable_float32":
		switch {
		case m.Default == nil:
			return ""
		case strings.HasPrefix(*m.Default, "constant."):
			return constRef(*m.Default)
		default:
			return *m.Default + "f"
		}
	case m.Type == "float64" || m.Type == "float64_supertype":
		switch {
		case m.Default == nil:
			return ""
		case strings.HasPrefix(*m.Default, "constant."):
			return constRef(*m.Default)
		default:
			return *m.Default
		}
	case m.Type == "bool":
		switch {
		case m.Default == nil, strings.EqualFold(*m.Default, "false"):
			return ""
		case strings.EqualFold(*m.Default, "true"):
			return "WGPUBool.True"
		case strings.HasPrefix(*m.Default, "constant."):
			return constRef(*m.Default)
		default:
			panic("unknown bool default: " + *m.Default)
		}
	case strings.HasPrefix(m.Type, "struct."):
		switch {
		case m.Optional:
			return ""
		case m.Default == nil:
			return g.CSType(m.Type, "") + ".Init"
		case *m.Default == "zero":
			return ""
		default:
			panic("unknown default for struct type")
		}
	case m.Default != nil:
		panic(fmt.Errorf("type %s should not have a default", m.Type))
	case m.Type == "out_string", m.Type == "string_with_default_empty", m.Type == "nullable_string":
		return "WGPUStringView.Init"
	default:
		// callbacks, objects, arrays and c_void are zero/null by default
		return ""
	}
}

// CSStructInit returns the "field = value" assignments reproducing the C INIT
// macro (zero-valued fields are omitted).
func (g *Generator) CSStructInit(s Struct) []string {
	var inits []string
	if s.Type == "extension" {
		inits = append(inits, fmt.Sprintf("chain = new WGPUChainedStruct { sType = WGPUSType.%s }", g.PascalCaseName(s.Base)))
	}
	for _, m := range s.Members {
		if arrayTypeRegexp.MatchString(m.Type) {
			continue
		}
		if v := g.csDefaultValue(m); v != "" {
			inits = append(inits, fmt.Sprintf("%s = %s", CSIdent(CamelCase(m.Name)), v))
		}
	}
	return inits
}

func (g *Generator) CSFunctionReturns(f Function) string {
	if f.Callback != nil {
		return "WGPUFuture"
	}
	if f.Returns != nil {
		return g.CSType(f.Returns.Type, f.Returns.Pointer)
	}
	return "void"
}

func (g *Generator) CSFunctionArgs(f Function, o *Object) string {
	var args []string
	if o != nil {
		args = append(args, fmt.Sprintf("%s %s", g.CSType(o.Base, ""), CSIdent(CamelCase(o.Name))))
	}
	for _, arg := range f.Args {
		matches := arrayTypeRegexp.FindStringSubmatch(arg.Type)
		if len(matches) == 2 {
			args = append(args,
				fmt.Sprintf("nuint %s", CSIdent(CamelCase(Singularize(arg.Name))+"Count")),
				fmt.Sprintf("%s %s", g.CSType(matches[1], arg.Pointer), CSIdent(CamelCase(arg.Name))))
		} else {
			args = append(args, fmt.Sprintf("%s %s", g.CSType(arg.Type, arg.Pointer), CSIdent(CamelCase(arg.Name))))
		}
	}
	if f.Callback != nil {
		args = append(args, fmt.Sprintf("%sCallbackInfo callbackInfo", g.CSType(*f.Callback, "")))
	}
	return strings.Join(args, ", ")
}
