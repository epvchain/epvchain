
// +build ignore

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/internal/gen"
	"golang.org/x/text/internal/tag"
	"golang.org/x/text/unicode/cldr"
)

var (
	test = flag.Bool("test",
		false,
		"test existing tables; can be used to compare web data with package data.")
	outputFile = flag.String("output",
		"tables.go",
		"output file for generated tables")
)

var comment = []string{
	`
lang holds an alphabetically sorted list of ISO-639 language identifiers.
All entries are 4 bytes. The index of the identifier (divided by 4) is the language tag.
For 2-byte language identifiers, the two successive bytes have the following meaning:
    - if the first letter of the 2- and 3-letter ISO codes are the same:
      the second and third letter of the 3-letter ISO code.
    - otherwise: a 0 and a by 2 bits right-shifted index into altLangISO3.
For 3-byte language identifiers the 4th byte is 0.`,
	`
langNoIndex is a bit vector of all 3-letter language codes that are not used as an index
in lookup tables. The language ids for these language codes are derived directly
from the letters and are not consecutive.`,
	`
altLangISO3 holds an alphabetically sorted list of 3-letter language code alternatives
to 2-letter language codes that cannot be derived using the method described above.
Each 3-letter code is followed by its 1-byte langID.`,
	`
altLangIndex is used to convert indexes in altLangISO3 to langIDs.`,
	`
langAliasMap maps langIDs to their suggested replacements.`,
	`
script is an alphabetically sorted list of ISO 15924 codes. The index
of the script in the string, divided by 4, is the internal scriptID.`,
	`
isoRegionOffset needs to be added to the index of regionISO to obtain the regionID
for 2-letter ISO codes. (The first isoRegionOffset regionIDs are reserved for
the UN.M49 codes used for groups.)`,
	`
regionISO holds a list of alphabetically sorted 2-letter ISO region codes.
Each 2-letter codes is followed by two bytes with the following meaning:
    - [A-Z}{2}: the first letter of the 2-letter code plus these two 
                letters form the 3-letter ISO code.
    - 0, n:     index into altRegionISO3.`,
	`
regionTypes defines the status of a region for various standards.`,
	`
m49 maps regionIDs to UN.M49 codes. The first isoRegionOffset entries are
codes indicating collections of regions.`,
	`
m49Index gives indexes into fromM49 based on the three most significant bits
of a 10-bit UN.M49 code. To search an UN.M49 code in fromM49, search in
   fromM49[m49Index[msb39(code)]:m49Index[msb3(code)+1]]
for an entry where the first 7 bits match the 7 lsb of the UN.M49 code.
The region code is stored in the 9 lsb of the indexed value.`,
	`
fromM49 contains entries to map UN.M49 codes to regions. See m49Index for details.`,
	`
altRegionISO3 holds a list of 3-letter region codes that cannot be
mapped to 2-letter codes using the default algorithm. This is a short list.`,
	`
altRegionIDs holds a list of regionIDs the positions of which match those
of the 3-letter ISO codes in altRegionISO3.`,
	`
variantNumSpecialized is the number of specialized variants in variants.`,
	`
suppressScript is an index from langID to the dominant script for that language,
if it exists.  If a script is given, it should be suppressed from the language tag.`,
	`
likelyLang is a lookup table, indexed by langID, for the most likely
scripts and regions given incomplete information. If more entries exist for a
given language, region and script are the index and size respectively
of the list in likelyLangList.`,
	`
likelyLangList holds lists info associated with likelyLang.`,
	`
likelyRegion is a lookup table, indexed by regionID, for the most likely
languages and scripts given incomplete information. If more entries exist
for a given regionID, lang and script are the index and size respectively
of the list in likelyRegionList.
TODO: exclude containers and user-definable regions from the list.`,
	`
likelyRegionList holds lists info associated with likelyRegion.`,
	`
likelyScript is a lookup table, indexed by scriptID, for the most likely
languages and regions given a script.`,
	`
matchLang holds pairs of langIDs of base languages that are typically
mutually intelligible. Each pair is associated with a confidence and
whether the intelligibility goes one or both ways.`,
	`
matchScript holds pairs of scriptIDs where readers of one script
can typically also read the other. Each is associated with a confidence.`,
	`
nRegionGroups is the number of region groups.`,
	`
regionInclusion maps region identifiers to sets of regions in regionInclusionBits,
where each set holds all groupings that are directly connected in a region
containment graph.`,
	`
regionInclusionBits is an array of bit vectors where every vector represents
a set of region groupings.  These sets are used to compute the distance
between two regions for the purpose of language matching.`,
	`
regionInclusionNext marks, for each entry in regionInclusionBits, the set of
all groups that are reachable from the groups set in the respective entry.`,
}

func failOnError(e error) {
	if e != nil {
		log.Panic(e)
	}
}

type setType int

const (
	Indexed setType = 1 + iota 
	Linear
)

type stringSet struct {
	s              []string
	sorted, frozen bool

	update map[string]string
	typ    setType 
}

func (ss *stringSet) clone() stringSet {
	c := *ss
	c.s = append([]string(nil), c.s...)
	return c
}

func (ss *stringSet) setType(t setType) {
	if ss.typ != t && ss.typ != 0 {
		log.Panicf("type %d cannot be assigned as it was already %d", t, ss.typ)
	}
}

func (ss *stringSet) parse(s string) {
	scan := bufio.NewScanner(strings.NewReader(s))
	scan.Split(bufio.ScanWords)
	for scan.Scan() {
		ss.add(scan.Text())
	}
}

func (ss *stringSet) assertChangeable() {
	if ss.frozen {
		log.Panic("attempt to modify a frozen stringSet")
	}
}

func (ss *stringSet) add(s string) {
	ss.assertChangeable()
	ss.s = append(ss.s, s)
	ss.sorted = ss.frozen
}

func (ss *stringSet) freeze() {
	ss.compact()
	ss.frozen = true
}

func (ss *stringSet) compact() {
	if ss.sorted {
		return
	}
	a := ss.s
	sort.Strings(a)
	k := 0
	for i := 1; i < len(a); i++ {
		if a[k] != a[i] {
			a[k+1] = a[i]
			k++
		}
	}
	ss.s = a[:k+1]
	ss.sorted = ss.frozen
}

type funcSorter struct {
	fn func(a, b string) bool
	sort.StringSlice
}

func (s funcSorter) Less(i, j int) bool {
	return s.fn(s.StringSlice[i], s.StringSlice[j])
}

func (ss *stringSet) sortFunc(f func(a, b string) bool) {
	ss.compact()
	sort.Sort(funcSorter{f, sort.StringSlice(ss.s)})
}

func (ss *stringSet) remove(s string) {
	ss.assertChangeable()
	if i, ok := ss.find(s); ok {
		copy(ss.s[i:], ss.s[i+1:])
		ss.s = ss.s[:len(ss.s)-1]
	}
}

func (ss *stringSet) replace(ol, nu string) {
	ss.s[ss.index(ol)] = nu
	ss.sorted = ss.frozen
}

func (ss *stringSet) index(s string) int {
	ss.setType(Indexed)
	i, ok := ss.find(s)
	if !ok {
		if i < len(ss.s) {
			log.Panicf("find: item %q is not in list. Closest match is %q.", s, ss.s[i])
		}
		log.Panicf("find: item %q is not in list", s)

	}
	return i
}

func (ss *stringSet) find(s string) (int, bool) {
	ss.compact()
	i := sort.SearchStrings(ss.s, s)
	return i, i != len(ss.s) && ss.s[i] == s
}

func (ss *stringSet) slice() []string {
	ss.compact()
	return ss.s
}

func (ss *stringSet) updateLater(v, key string) {
	if ss.update == nil {
		ss.update = map[string]string{}
	}
	ss.update[v] = key
}

func (ss *stringSet) join() string {
	ss.setType(Indexed)
	n := len(ss.s[0])
	for _, s := range ss.s {
		if len(s) != n {
			log.Panicf("join: not all entries are of the same length: %q", s)
		}
	}
	ss.s = append(ss.s, strings.Repeat("\xff", n))
	return strings.Join(ss.s, "")
}

type ianaEntry struct {
	typ            string
	description    []string
	scope          string
	added          string
	preferred      string
	deprecated     string
	suppressScript string
	macro          string
	prefix         []string
}

type builder struct {
	w    *gen.CodeWriter
	hw   io.Writer 
	data *cldr.CLDR
	supp *cldr.SupplementalData

	locale      stringSet 
	lang        stringSet 
	langNoIndex stringSet 
	script      stringSet 
	region      stringSet 
	variant     stringSet 

	groups map[int]index

	registry map[string]*ianaEntry
}

type index uint

func newBuilder(w *gen.CodeWriter) *builder {
	r := gen.OpenCLDRCoreZip()
	defer r.Close()
	d := &cldr.Decoder{}
	data, err := d.DecodeZip(r)
	failOnError(err)
	b := builder{
		w:    w,
		hw:   io.MultiWriter(w, w.Hash),
		data: data,
		supp: data.Supplemental(),
	}
	b.parseRegistry()
	return &b
}

func (b *builder) parseRegistry() {
	r := gen.OpenIANAFile("assignments/language-subtag-registry")
	defer r.Close()
	b.registry = make(map[string]*ianaEntry)

	scan := bufio.NewScanner(r)
	scan.Split(bufio.ScanWords)
	var record *ianaEntry
	for more := scan.Scan(); more; {
		key := scan.Text()
		more = scan.Scan()
		value := scan.Text()
		switch key {
		case "Type:":
			record = &ianaEntry{typ: value}
		case "Subtag:", "Tag:":
			if s := strings.SplitN(value, "..", 2); len(s) > 1 {
				for a := s[0]; a <= s[1]; a = inc(a) {
					b.addToRegistry(a, record)
				}
			} else {
				b.addToRegistry(value, record)
			}
		case "Suppress-Script:":
			record.suppressScript = value
		case "Added:":
			record.added = value
		case "Deprecated:":
			record.deprecated = value
		case "Macrolanguage:":
			record.macro = value
		case "Preferred-Value:":
			record.preferred = value
		case "Prefix:":
			record.prefix = append(record.prefix, value)
		case "Scope:":
			record.scope = value
		case "Description:":
			buf := []byte(value)
			for more = scan.Scan(); more; more = scan.Scan() {
				b := scan.Bytes()
				if b[0] == '%' || b[len(b)-1] == ':' {
					break
				}
				buf = append(buf, ' ')
				buf = append(buf, b...)
			}
			record.description = append(record.description, string(buf))
			continue
		default:
			continue
		}
		more = scan.Scan()
	}
	if scan.Err() != nil {
		log.Panic(scan.Err())
	}
}

func (b *builder) addToRegistry(key string, entry *ianaEntry) {
	if info, ok := b.registry[key]; ok {
		if info.typ != "language" || entry.typ != "extlang" {
			log.Fatalf("parseRegistry: tag %q already exists", key)
		}
	} else {
		b.registry[key] = entry
	}
}

var commentIndex = make(map[string]string)

func init() {
	for _, s := range comment {
		key := strings.TrimSpace(strings.SplitN(s, " ", 2)[0])
		commentIndex[key] = s
	}
}

func (b *builder) comment(name string) {
	if s := commentIndex[name]; len(s) > 0 {
		b.w.WriteComment(s)
	} else {
		fmt.Fprintln(b.w)
	}
}

func (b *builder) pf(f string, x ...interface{}) {
	fmt.Fprintf(b.hw, f, x...)
	fmt.Fprint(b.hw, "\n")
}

func (b *builder) p(x ...interface{}) {
	fmt.Fprintln(b.hw, x...)
}

func (b *builder) addSize(s int) {
	b.w.Size += s
	b.pf("// Size: %d bytes", s)
}

func (b *builder) writeConst(name string, x interface{}) {
	b.comment(name)
	b.w.WriteConst(name, x)
}

func (b *builder) writeConsts(f func(string) int, values ...string) {
	b.pf("const (")
	for _, v := range values {
		b.pf("\t_%s = %v", v, f(v))
	}
	b.pf(")")
}

func (b *builder) writeType(value interface{}) {
	b.comment(reflect.TypeOf(value).Name())
	b.w.WriteType(value)
}

func (b *builder) writeSlice(name string, ss interface{}) {
	b.writeSliceAddSize(name, 0, ss)
}

func (b *builder) writeSliceAddSize(name string, extraSize int, ss interface{}) {
	b.comment(name)
	b.w.Size += extraSize
	v := reflect.ValueOf(ss)
	t := v.Type().Elem()
	b.pf("// Size: %d bytes, %d elements", v.Len()*int(t.Size())+extraSize, v.Len())

	fmt.Fprintf(b.w, "var %s = ", name)
	b.w.WriteArray(ss)
	b.p()
}

type fromTo struct {
	from, to uint16
}

func (b *builder) writeSortedMap(name string, ss *stringSet, index func(s string) uint16) {
	ss.sortFunc(func(a, b string) bool {
		return index(a) < index(b)
	})
	m := []fromTo{}
	for _, s := range ss.s {
		m = append(m, fromTo{index(s), index(ss.update[s])})
	}
	b.writeSlice(name, m)
}

const base = 'z' - 'a' + 1

func strToInt(s string) uint {
	v := uint(0)
	for i := 0; i < len(s); i++ {
		v *= base
		v += uint(s[i] - 'a')
	}
	return v
}

func intToStr(v uint, s []byte) {
	for i := len(s) - 1; i >= 0; i-- {
		s[i] = byte(v%base) + 'a'
		v /= base
	}
}

func (b *builder) writeBitVector(name string, ss []string) {
	vec := make([]uint8, int(math.Ceil(math.Pow(base, float64(len(ss[0])))/8)))
	for _, s := range ss {
		v := strToInt(s)
		vec[v/8] |= 1 << (v % 8)
	}
	b.writeSlice(name, vec)
}

func (b *builder) writeMapFunc(name string, m map[string]string, f func(string) uint16) {
	b.comment(name)
	v := reflect.ValueOf(m)
	sz := v.Len() * (2 + int(v.Type().Key().Size()))
	for _, k := range m {
		sz += len(k)
	}
	b.addSize(sz)
	keys := []string{}
	b.pf(`var %s = map[string]uint16{`, name)
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.pf("\t%q: %v,", k, f(m[k]))
	}
	b.p("}")
}

func (b *builder) writeMap(name string, m interface{}) {
	b.comment(name)
	v := reflect.ValueOf(m)
	sz := v.Len() * (2 + int(v.Type().Key().Size()) + int(v.Type().Elem().Size()))
	b.addSize(sz)
	f := strings.FieldsFunc(fmt.Sprintf("%#v", m), func(r rune) bool {
		return strings.IndexRune("{}, ", r) != -1
	})
	sort.Strings(f[1:])
	b.pf(`var %s = %s{`, name, f[0])
	for _, kv := range f[1:] {
		b.pf("\t%s,", kv)
	}
	b.p("}")
}

func (b *builder) langIndex(s string) uint16 {
	if s == "und" {
		return 0
	}
	if i, ok := b.lang.find(s); ok {
		return uint16(i)
	}
	return uint16(strToInt(s)) + uint16(len(b.lang.s))
}

func inc(s string) string {
	const maxTagLength = 4
	var buf [maxTagLength]byte
	intToStr(strToInt(strings.ToLower(s))+1, buf[:len(s)])
	for i := 0; i < len(s); i++ {
		if s[i] <= 'Z' {
			buf[i] -= 'a' - 'A'
		}
	}
	return string(buf[:len(s)])
}

func (b *builder) parseIndices() {
	meta := b.supp.Metadata

	for k, v := range b.registry {
		var ss *stringSet
		switch v.typ {
		case "language":
			if len(k) == 2 || v.suppressScript != "" || v.scope == "special" {
				b.lang.add(k)
				continue
			} else {
				ss = &b.langNoIndex
			}
		case "region":
			ss = &b.region
		case "script":
			ss = &b.script
		case "variant":
			ss = &b.variant
		default:
			continue
		}
		ss.add(k)
	}

	for _, lang := range b.data.Locales() {
		if x := b.data.RawLDML(lang); false ||
			x.LocaleDisplayNames != nil ||
			x.Characters != nil ||
			x.Delimiters != nil ||
			x.Measurement != nil ||
			x.Dates != nil ||
			x.Numbers != nil ||
			x.Units != nil ||
			x.ListPatterns != nil ||
			x.Collations != nil ||
			x.Segmentations != nil ||
			x.Rbnf != nil ||
			x.Annotations != nil ||
			x.Metadata != nil {

			from := strings.Split(lang, "_")
			if lang := from[0]; lang != "root" {
				b.lang.add(lang)
			}
		}
	}

	for _, plurals := range b.data.Supplemental().Plurals {
		for _, rules := range plurals.PluralRules {
			for _, lang := range strings.Split(rules.Locales, " ") {
				if lang = strings.Split(lang, "_")[0]; lang != "root" {
					b.lang.add(lang)
				}
			}
		}
	}

	for _, m := range b.supp.LikelySubtags.LikelySubtag {
		from := strings.Split(m.From, "_")
		b.lang.add(from[0])
	}

	for _, a := range meta.Alias.LanguageAlias {
		if a.Reason == "bibliographic" {
			b.langNoIndex.add(a.Type)
		}
	}

	for _, reg := range b.supp.Metadata.Alias.TerritoryAlias {
		if len(reg.Type) == 2 {
			b.region.add(reg.Type)
		}
	}

	for _, s := range b.lang.s {
		if len(s) == 3 {
			b.langNoIndex.remove(s)
		}
	}
	b.writeConst("numLanguages", len(b.lang.slice())+len(b.langNoIndex.slice()))
	b.writeConst("numScripts", len(b.script.slice()))
	b.writeConst("numRegions", len(b.region.slice()))

	b.lang.add("---")
	b.script.add("----")
	b.region.add("---")

	b.locale.parse(meta.DefaultContent.Locales)
}

func (b *builder) computeRegionGroups() {
	b.groups = make(map[int]index)

	for i := 1; b.region.s[i][0] < 'A'; i++ { 
		b.groups[i] = index(len(b.groups))
	}
	for _, g := range b.supp.TerritoryContainment.Group {

		if g.Type == "EZ" || g.Type == "UN" {
			continue
		}
		group := b.region.index(g.Type)
		if _, ok := b.groups[group]; !ok {
			b.groups[group] = index(len(b.groups))
		}
	}
	if len(b.groups) > 32 {
		log.Fatalf("only 32 groups supported, found %d", len(b.groups))
	}
	b.writeConst("nRegionGroups", len(b.groups))
}

var langConsts = []string{
	"af", "am", "ar", "az", "bg", "bn", "ca", "cs", "da", "de", "el", "en", "es",
	"et", "fa", "fi", "fil", "fr", "gu", "he", "hi", "hr", "hu", "hy", "id", "is",
	"it", "ja", "ka", "kk", "km", "kn", "ko", "ky", "lo", "lt", "lv", "mk", "ml",
	"mn", "mo", "mr", "ms", "mul", "my", "nb", "ne", "nl", "no", "pa", "pl", "pt",
	"ro", "ru", "sh", "si", "sk", "sl", "sq", "sr", "sv", "sw", "ta", "te", "th",
	"tl", "tn", "tr", "uk", "ur", "uz", "vi", "zh", "zu",

	"jbo", "ami", "bnn", "hak", "tlh", "lb", "nv", "pwn", "tao", "tay", "tsu",
	"nn", "sfb", "vgt", "sgg", "cmn", "nan", "hsn",
}

func (b *builder) writeLanguage() {
	meta := b.supp.Metadata

	b.writeConst("nonCanonicalUnd", b.lang.index("und"))
	b.writeConsts(func(s string) int { return int(b.langIndex(s)) }, langConsts...)
	b.writeConst("langPrivateStart", b.langIndex("qaa"))
	b.writeConst("langPrivateEnd", b.langIndex("qtz"))

	langAliasMap := stringSet{}
	aliasTypeMap := map[string]langAliasType{}

	altLangISO3 := stringSet{}

	altLangISO3.add("---")
	altLangISO3.updateLater("---", "aa")

	lang := b.lang.clone()
	for _, a := range meta.Alias.LanguageAlias {
		if a.Replacement == "" {
			a.Replacement = "und"
		}

		repl := strings.SplitN(a.Replacement, "_", 2)[0]
		if a.Reason == "overlong" {
			if len(a.Replacement) == 2 && len(a.Type) == 3 {
				lang.updateLater(a.Replacement, a.Type)
			}
		} else if len(a.Type) <= 3 {
			switch a.Reason {
			case "macrolanguage":
				aliasTypeMap[a.Type] = langMacro
			case "deprecated":

				continue
			case "bibliographic", "legacy":
				if a.Type == "no" {
					continue
				}
				aliasTypeMap[a.Type] = langLegacy
			default:
				log.Fatalf("new %s alias: %s", a.Reason, a.Type)
			}
			langAliasMap.add(a.Type)
			langAliasMap.updateLater(a.Type, repl)
		}
	}

	langAliasMap.add("nb")
	langAliasMap.updateLater("nb", "no")
	aliasTypeMap["nb"] = langMacro

	for k, v := range b.registry {

		if v.typ == "language" && v.deprecated != "" && v.preferred != "" {
			langAliasMap.add(k)
			langAliasMap.updateLater(k, v.preferred)
			aliasTypeMap[k] = langDeprecated
		}
	}

	lang.updateLater("tl", "tgl")
	lang.updateLater("sh", "hbs")
	lang.updateLater("mo", "mol")
	lang.updateLater("no", "nor")
	lang.updateLater("tw", "twi")
	lang.updateLater("nb", "nob")
	lang.updateLater("ak", "aka")
	lang.updateLater("bh", "bih")

	for _, v := range lang.s[1:] {
		s, ok := lang.update[v]
		if !ok {
			if s, ok = lang.update[langAliasMap.update[v]]; !ok {
				continue
			}
			lang.update[v] = s
		}
		if v[0] != s[0] {
			altLangISO3.add(s)
			altLangISO3.updateLater(s, v)
		}
	}

	lang.freeze()
	for i, v := range lang.s {

		add := ""
		if s, ok := lang.update[v]; ok {
			if s[0] == v[0] {
				add = s[1:]
			} else {
				add = string([]byte{0, byte(altLangISO3.index(s))})
			}
		} else if len(v) == 3 {
			add = "\x00"
		} else {
			log.Panicf("no data for long form of %q", v)
		}
		lang.s[i] += add
	}
	b.writeConst("lang", tag.Index(lang.join()))

	b.writeConst("langNoIndexOffset", len(b.lang.s))

	b.writeBitVector("langNoIndex", b.langNoIndex.slice())

	altLangIndex := []uint16{}
	for i, s := range altLangISO3.slice() {
		altLangISO3.s[i] += string([]byte{byte(len(altLangIndex))})
		if i > 0 {
			idx := b.lang.index(altLangISO3.update[s])
			altLangIndex = append(altLangIndex, uint16(idx))
		}
	}
	b.writeConst("altLangISO3", tag.Index(altLangISO3.join()))
	b.writeSlice("altLangIndex", altLangIndex)

	b.writeSortedMap("langAliasMap", &langAliasMap, b.langIndex)
	types := make([]langAliasType, len(langAliasMap.s))
	for i, s := range langAliasMap.s {
		types[i] = aliasTypeMap[s]
	}
	b.writeSlice("langAliasTypes", types)
}

var scriptConsts = []string{
	"Latn", "Hani", "Hans", "Hant", "Qaaa", "Qaai", "Qabx", "Zinh", "Zyyy",
	"Zzzz",
}

func (b *builder) writeScript() {
	b.writeConsts(b.script.index, scriptConsts...)
	b.writeConst("script", tag.Index(b.script.join()))

	supp := make([]uint8, len(b.lang.slice()))
	for i, v := range b.lang.slice()[1:] {
		if sc := b.registry[v].suppressScript; sc != "" {
			supp[i+1] = uint8(b.script.index(sc))
		}
	}
	b.writeSlice("suppressScript", supp)

	for _, a := range b.supp.Metadata.Alias.ScriptAlias {
		if a.Type != "Qaai" {
			log.Panicf("unexpected deprecated stript %q", a.Type)
		}
	}
}

func parseM49(s string) int16 {
	if len(s) == 0 {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 10)
	failOnError(err)
	return int16(v)
}

var regionConsts = []string{
	"001", "419", "BR", "CA", "ES", "GB", "MD", "PT", "UK", "US",
	"ZZ", "XA", "XC", "XK", 
}

func (b *builder) writeRegion() {
	b.writeConsts(b.region.index, regionConsts...)

	isoOffset := b.region.index("AA")
	m49map := make([]int16, len(b.region.slice()))
	fromM49map := make(map[int16]int)
	altRegionISO3 := ""
	altRegionIDs := []uint16{}

	b.writeConst("isoRegionOffset", isoOffset)

	regionISO := b.region.clone()
	regionISO.s = regionISO.s[isoOffset:]
	regionISO.sorted = false

	regionTypes := make([]byte, len(b.region.s))

	for s, e := range b.registry {
		if len(s) == 2 && s == strings.ToUpper(s) {
			i := b.region.index(s)
			for _, d := range e.description {
				if strings.Contains(d, "Private use") {
					regionTypes[i] = iso3166UserAssgined
				}
			}
			regionTypes[i] |= bcp47Region
		}
	}

	r := gen.OpenIANAFile("domains/root/db")
	defer r.Close()

	buf, err := ioutil.ReadAll(r)
	failOnError(err)
	re := regexp.MustCompile(`"/domains/root/db/([a-z]{2}).html"`)
	for _, m := range re.FindAllSubmatch(buf, -1) {
		i := b.region.index(strings.ToUpper(string(m[1])))
		regionTypes[i] |= ccTLD
	}

	b.writeSlice("regionTypes", regionTypes)

	iso3Set := make(map[string]int)
	update := func(iso2, iso3 string) {
		i := regionISO.index(iso2)
		if j, ok := iso3Set[iso3]; !ok && iso3[0] == iso2[0] {
			regionISO.s[i] += iso3[1:]
			iso3Set[iso3] = -1
		} else {
			if ok && j >= 0 {
				regionISO.s[i] += string([]byte{0, byte(j)})
			} else {
				iso3Set[iso3] = len(altRegionISO3)
				regionISO.s[i] += string([]byte{0, byte(len(altRegionISO3))})
				altRegionISO3 += iso3
				altRegionIDs = append(altRegionIDs, uint16(isoOffset+i))
			}
		}
	}
	for _, tc := range b.supp.CodeMappings.TerritoryCodes {
		i := regionISO.index(tc.Type) + isoOffset
		if d := m49map[i]; d != 0 {
			log.Panicf("%s found as a duplicate UN.M49 code of %03d", tc.Numeric, d)
		}
		m49 := parseM49(tc.Numeric)
		m49map[i] = m49
		if r := fromM49map[m49]; r == 0 {
			fromM49map[m49] = i
		} else if r != i {
			dep := b.registry[regionISO.s[r-isoOffset]].deprecated
			if t := b.registry[tc.Type]; t != nil && dep != "" && (t.deprecated == "" || t.deprecated > dep) {
				fromM49map[m49] = i
			}
		}
	}
	for _, ta := range b.supp.Metadata.Alias.TerritoryAlias {
		if len(ta.Type) == 3 && ta.Type[0] <= '9' && len(ta.Replacement) == 2 {
			from := parseM49(ta.Type)
			if r := fromM49map[from]; r == 0 {
				fromM49map[from] = regionISO.index(ta.Replacement) + isoOffset
			}
		}
	}
	for _, tc := range b.supp.CodeMappings.TerritoryCodes {
		if len(tc.Alpha3) == 3 {
			update(tc.Type, tc.Alpha3)
		}
	}

	for _, m := range []struct{ iso2, iso3 string }{
		{"CT", "CTE"},
		{"DY", "DHY"},
		{"HV", "HVO"},
		{"JT", "JTN"},
		{"MI", "MID"},
		{"NH", "NHB"},
		{"NQ", "ATN"},
		{"PC", "PCI"},
		{"PU", "PUS"},
		{"PZ", "PCZ"},
		{"RH", "RHO"},
		{"VD", "VDR"},
		{"WK", "WAK"},

		{"FQ", "ATF"},
	} {
		update(m.iso2, m.iso3)
	}
	for i, s := range regionISO.s {
		if len(s) != 4 {
			regionISO.s[i] = s + "  "
		}
	}
	b.writeConst("regionISO", tag.Index(regionISO.join()))
	b.writeConst("altRegionISO3", altRegionISO3)
	b.writeSlice("altRegionIDs", altRegionIDs)

	regionOldMap := stringSet{}

	for _, reg := range b.supp.Metadata.Alias.TerritoryAlias {
		if len(reg.Type) == 2 && reg.Reason == "deprecated" && len(reg.Replacement) == 2 {
			regionOldMap.add(reg.Type)
			regionOldMap.updateLater(reg.Type, reg.Replacement)
			i, _ := regionISO.find(reg.Type)
			j, _ := regionISO.find(reg.Replacement)
			if k := m49map[i+isoOffset]; k == 0 {
				m49map[i+isoOffset] = m49map[j+isoOffset]
			}
		}
	}
	b.writeSortedMap("regionOldMap", &regionOldMap, func(s string) uint16 {
		return uint16(b.region.index(s))
	})

	for i := 1; i < isoOffset; i++ {
		m := parseM49(b.region.s[i])
		m49map[i] = m
		fromM49map[m] = i
	}
	b.writeSlice("m49", m49map)

	const (
		searchBits = 7
		regionBits = 9
	)
	if len(m49map) >= 1<<regionBits {
		log.Fatalf("Maximum number of regions exceeded: %d > %d", len(m49map), 1<<regionBits)
	}
	m49Index := [9]int16{}
	fromM49 := []uint16{}
	m49 := []int{}
	for k, _ := range fromM49map {
		m49 = append(m49, int(k))
	}
	sort.Ints(m49)
	for _, k := range m49[1:] {
		val := (k & (1<<searchBits - 1)) << regionBits
		fromM49 = append(fromM49, uint16(val|fromM49map[int16(k)]))
		m49Index[1:][k>>searchBits] = int16(len(fromM49))
	}
	b.writeSlice("m49Index", m49Index)
	b.writeSlice("fromM49", fromM49)
}

const (

	iso3166Except = "AC CP DG EA EU FX IC SU TA UK"
	iso3166Trans  = "AN BU CS NT TP YU ZR" 

	iso3166DelCLDR = "CT DD DY FQ HV JT MI NH NQ PC PU PZ RH VD WK YD"
)

const (
	iso3166UserAssgined = 1 << iota
	ccTLD
	bcp47Region
)

func find(list []string, s string) int {
	for i, t := range list {
		if t == s {
			return i
		}
	}
	return -1
}

func (b *builder) writeVariant() {
	generalized := stringSet{}
	specialized := stringSet{}
	specializedExtend := stringSet{}

	for _, v := range b.variant.slice() {
		e := b.registry[v]
		if len(e.prefix) == 0 {
			generalized.add(v)
			continue
		}
		c := strings.Split(e.prefix[0], "-")
		hasScriptOrRegion := false
		if len(c) > 1 {
			_, hasScriptOrRegion = b.script.find(c[1])
			if !hasScriptOrRegion {
				_, hasScriptOrRegion = b.region.find(c[1])

			}
		}
		if len(c) == 1 || len(c) == 2 && hasScriptOrRegion {

			specialized.add(v)
			continue
		}

		specializedExtend.add(v)
		prefix := c[0] + "-"
		if hasScriptOrRegion {
			prefix += c[1]
		}
		for _, p := range e.prefix {

			i := strings.LastIndex(p, "-")
			pred := b.registry[p[i+1:]]
			if find(pred.prefix, p[:i]) < 0 {
				log.Fatalf("prefix %q for variant %q not consistent with predecessor spec", p, v)
			}

			count := strings.Count(p[:i], "-")
			for _, q := range pred.prefix {
				if c := strings.Count(q, "-"); c != count {
					log.Fatalf("variant %q preceding %q has a prefix %q of size %d; want %d", p[i+1:], v, q, c, count)
				}
			}
			if !strings.HasPrefix(p, prefix) {
				log.Fatalf("prefix %q of variant %q should start with %q", p, v, prefix)
			}
		}
	}

	a := specializedExtend.s
	less := func(v, w string) bool {

		maxCount := func(s string) (max int) {
			for _, p := range b.registry[s].prefix {
				if c := strings.Count(p, "-"); c > max {
					max = c
				}
			}
			return
		}
		if cv, cw := maxCount(v), maxCount(w); cv != cw {
			return cv < cw
		}

		return v < w
	}
	sort.Sort(funcSorter{less, sort.StringSlice(a)})
	specializedExtend.frozen = true

	variantIndex := make(map[string]uint8)
	add := func(s []string) {
		for _, v := range s {
			variantIndex[v] = uint8(len(variantIndex))
		}
	}
	add(specialized.slice())
	add(specializedExtend.s)
	numSpecialized := len(variantIndex)
	add(generalized.slice())
	if n := len(variantIndex); n > 255 {
		log.Fatalf("maximum number of variants exceeded: was %d; want <= 255", n)
	}
	b.writeMap("variantIndex", variantIndex)
	b.writeConst("variantNumSpecialized", numSpecialized)
}

func (b *builder) writeLanguageInfo() {
}

func (b *builder) writeLikelyData() {
	const (
		isList = 1 << iota
		scriptInFrom
		regionInFrom
	)
	type ( 
		likelyScriptRegion struct {
			region uint16
			script uint8
			flags  uint8
		}
		likelyLangScript struct {
			lang   uint16
			script uint8
			flags  uint8
		}
		likelyLangRegion struct {
			lang   uint16
			region uint16
		}

		likelyTag struct {
			lang   uint16
			region uint16
			script uint8
		}
	)
	var ( 
		likelyRegionGroup = make([]likelyTag, len(b.groups))
		likelyLang        = make([]likelyScriptRegion, len(b.lang.s))
		likelyRegion      = make([]likelyLangScript, len(b.region.s))
		likelyScript      = make([]likelyLangRegion, len(b.script.s))
		likelyLangList    = []likelyScriptRegion{}
		likelyRegionList  = []likelyLangScript{}
	)
	type fromTo struct {
		from, to []string
	}
	langToOther := map[int][]fromTo{}
	regionToOther := map[int][]fromTo{}
	for _, m := range b.supp.LikelySubtags.LikelySubtag {
		from := strings.Split(m.From, "_")
		to := strings.Split(m.To, "_")
		if len(to) != 3 {
			log.Fatalf("invalid number of subtags in %q: found %d, want 3", m.To, len(to))
		}
		if len(from) > 3 {
			log.Fatalf("invalid number of subtags: found %d, want 1-3", len(from))
		}
		if from[0] != to[0] && from[0] != "und" {
			log.Fatalf("unexpected language change in expansion: %s -> %s", from, to)
		}
		if len(from) == 3 {
			if from[2] != to[2] {
				log.Fatalf("unexpected region change in expansion: %s -> %s", from, to)
			}
			if from[0] != "und" {
				log.Fatalf("unexpected fully specified from tag: %s -> %s", from, to)
			}
		}
		if len(from) == 1 || from[0] != "und" {
			id := 0
			if from[0] != "und" {
				id = b.lang.index(from[0])
			}
			langToOther[id] = append(langToOther[id], fromTo{from, to})
		} else if len(from) == 2 && len(from[1]) == 4 {
			sid := b.script.index(from[1])
			likelyScript[sid].lang = uint16(b.langIndex(to[0]))
			likelyScript[sid].region = uint16(b.region.index(to[2]))
		} else {
			r := b.region.index(from[len(from)-1])
			if id, ok := b.groups[r]; ok {
				if from[0] != "und" {
					log.Fatalf("region changed unexpectedly: %s -> %s", from, to)
				}
				likelyRegionGroup[id].lang = uint16(b.langIndex(to[0]))
				likelyRegionGroup[id].script = uint8(b.script.index(to[1]))
				likelyRegionGroup[id].region = uint16(b.region.index(to[2]))
			} else {
				regionToOther[r] = append(regionToOther[r], fromTo{from, to})
			}
		}
	}
	b.writeType(likelyLangRegion{})
	b.writeSlice("likelyScript", likelyScript)

	for id := range b.lang.s {
		list := langToOther[id]
		if len(list) == 1 {
			likelyLang[id].region = uint16(b.region.index(list[0].to[2]))
			likelyLang[id].script = uint8(b.script.index(list[0].to[1]))
		} else if len(list) > 1 {
			likelyLang[id].flags = isList
			likelyLang[id].region = uint16(len(likelyLangList))
			likelyLang[id].script = uint8(len(list))
			for _, x := range list {
				flags := uint8(0)
				if len(x.from) > 1 {
					if x.from[1] == x.to[2] {
						flags = regionInFrom
					} else {
						flags = scriptInFrom
					}
				}
				likelyLangList = append(likelyLangList, likelyScriptRegion{
					region: uint16(b.region.index(x.to[2])),
					script: uint8(b.script.index(x.to[1])),
					flags:  flags,
				})
			}
		}
	}

	b.writeType(likelyScriptRegion{})
	b.writeSlice("likelyLang", likelyLang)
	b.writeSlice("likelyLangList", likelyLangList)

	for id := range b.region.s {
		list := regionToOther[id]
		if len(list) == 1 {
			likelyRegion[id].lang = uint16(b.langIndex(list[0].to[0]))
			likelyRegion[id].script = uint8(b.script.index(list[0].to[1]))
			if len(list[0].from) > 2 {
				likelyRegion[id].flags = scriptInFrom
			}
		} else if len(list) > 1 {
			likelyRegion[id].flags = isList
			likelyRegion[id].lang = uint16(len(likelyRegionList))
			likelyRegion[id].script = uint8(len(list))
			for i, x := range list {
				if len(x.from) == 2 && i != 0 || i > 0 && len(x.from) != 3 {
					log.Fatalf("unspecified script must be first in list: %v at %d", x.from, i)
				}
				x := likelyLangScript{
					lang:   uint16(b.langIndex(x.to[0])),
					script: uint8(b.script.index(x.to[1])),
				}
				if len(list[0].from) > 2 {
					x.flags = scriptInFrom
				}
				likelyRegionList = append(likelyRegionList, x)
			}
		}
	}
	b.writeType(likelyLangScript{})
	b.writeSlice("likelyRegion", likelyRegion)
	b.writeSlice("likelyRegionList", likelyRegionList)

	b.writeType(likelyTag{})
	b.writeSlice("likelyRegionGroup", likelyRegionGroup)
}

type mutualIntelligibility struct {
	want, have uint16
	conf       uint8
	oneway     bool
}

type scriptIntelligibility struct {
	lang       uint16 
	want, have uint8
	conf       uint8
}

type sortByConf []mutualIntelligibility

func (l sortByConf) Less(a, b int) bool {
	return l[a].conf > l[b].conf
}

func (l sortByConf) Swap(a, b int) {
	l[a], l[b] = l[b], l[a]
}

func (l sortByConf) Len() int {
	return len(l)
}

func toConf(pct uint8) uint8 {
	switch {
	case pct == 100:
		return 3 
	case pct >= 90:
		return 2 
	case pct > 50:
		return 1 
	default:
		return 0 
	}
}

func (b *builder) writeMatchData() {
	b.writeType(mutualIntelligibility{})
	b.writeType(scriptIntelligibility{})
	lm := b.supp.LanguageMatching.LanguageMatches
	cldr.MakeSlice(&lm).SelectAnyOf("type", "written")

	matchLang := []mutualIntelligibility{}
	matchScript := []scriptIntelligibility{}

	for _, m := range lm[0].LanguageMatch {

		desired := strings.Replace(m.Desired, "-", "_", -1)
		supported := strings.Replace(m.Supported, "-", "_", -1)
		d := strings.Split(desired, "_")
		s := strings.Split(supported, "_")
		if len(d) != len(s) || len(d) > 2 {

			continue
		}
		pct, _ := strconv.ParseInt(m.Percent, 10, 8)
		if len(d) == 2 && d[0] == s[0] && len(d[1]) == 4 {

			lang := uint16(0)
			if d[0] != "*" {
				lang = uint16(b.langIndex(d[0]))
			}
			matchScript = append(matchScript, scriptIntelligibility{
				lang: lang,
				want: uint8(b.script.index(d[1])),
				have: uint8(b.script.index(s[1])),
				conf: toConf(uint8(pct)),
			})
			if m.Oneway != "true" {
				matchScript = append(matchScript, scriptIntelligibility{
					lang: lang,
					want: uint8(b.script.index(s[1])),
					have: uint8(b.script.index(d[1])),
					conf: toConf(uint8(pct)),
				})
			}
		} else if len(d) == 1 && d[0] != "*" {
			if pct == 100 {

				if d[0] != "no" || s[0] != "nb" {
					log.Fatalf("unhandled equivalence %s == %s", s[0], d[0])
				}
				continue
			}
			matchLang = append(matchLang, mutualIntelligibility{
				want:   uint16(b.langIndex(d[0])),
				have:   uint16(b.langIndex(s[0])),
				conf:   uint8(pct),
				oneway: m.Oneway == "true",
			})
		} else {

			a := []string{"*;*", "*_*;*_*", "es_MX;es_419"}
			s := strings.Join([]string{desired, supported}, ";")
			if i := sort.SearchStrings(a, s); i == len(a) || a[i] != s {
				log.Printf("%q not handled", s)
			}
		}
	}
	sort.Stable(sortByConf(matchLang))

	for i, m := range matchLang {
		matchLang[i].conf = toConf(m.conf)
	}
	b.writeSlice("matchLang", matchLang)
	b.writeSlice("matchScript", matchScript)
}

func (b *builder) writeRegionInclusionData() {
	var (

		mm = make(map[int][]index)

		containment = make(map[index][]index)
	)
	for _, g := range b.supp.TerritoryContainment.Group {

		if g.Type == "EZ" || g.Type == "UN" {
			continue
		}
		group := b.region.index(g.Type)
		groupIdx := b.groups[group]
		for _, mem := range strings.Split(g.Contains, " ") {
			r := b.region.index(mem)
			mm[r] = append(mm[r], groupIdx)
			if g, ok := b.groups[r]; ok {
				mm[group] = append(mm[group], g)
				containment[groupIdx] = append(containment[groupIdx], g)
			}
		}
	}

	regionContainment := make([]uint32, len(b.groups))
	for _, g := range b.groups {
		l := containment[g]

		for i := 0; i < len(l); i++ {
			l = append(l, containment[l[i]]...)
		}

		regionContainment[g] = 1 << g
		for _, v := range l {
			regionContainment[g] |= 1 << v
		}

	}
	b.writeSlice("regionContainment", regionContainment)

	regionInclusion := make([]uint8, len(b.region.s))
	bvs := make(map[uint32]index)

	for r, i := range b.groups {
		bv := uint32(1 << i)
		for _, g := range mm[r] {
			bv |= 1 << g
		}
		bvs[bv] = i
		regionInclusion[r] = uint8(bvs[bv])
	}
	for r := 1; r < len(b.region.s); r++ {
		if _, ok := b.groups[r]; !ok {
			bv := uint32(0)
			for _, g := range mm[r] {
				bv |= 1 << g
			}
			if bv == 0 {

				bv = 1 << b.groups[b.region.index("001")]
			}
			if _, ok := bvs[bv]; !ok {
				bvs[bv] = index(len(bvs))
			}
			regionInclusion[r] = uint8(bvs[bv])
		}
	}
	b.writeSlice("regionInclusion", regionInclusion)
	regionInclusionBits := make([]uint32, len(bvs))
	for k, v := range bvs {
		regionInclusionBits[v] = uint32(k)
	}

	regionInclusionNext := []uint8{}
	for i := 0; i < len(regionInclusionBits); i++ {
		bits := regionInclusionBits[i]
		next := bits
		for i := uint(0); i < uint(len(b.groups)); i++ {
			if bits&(1<<i) != 0 {
				next |= regionInclusionBits[i]
			}
		}
		if _, ok := bvs[next]; !ok {
			bvs[next] = index(len(bvs))
			regionInclusionBits = append(regionInclusionBits, next)
		}
		regionInclusionNext = append(regionInclusionNext, uint8(bvs[next]))
	}
	b.writeSlice("regionInclusionBits", regionInclusionBits)
	b.writeSlice("regionInclusionNext", regionInclusionNext)
}

type parentRel struct {
	lang       uint16
	script     uint8
	maxScript  uint8
	toRegion   uint16
	fromRegion []uint16
}

func (b *builder) writeParents() {
	b.writeType(parentRel{})

	parents := []parentRel{}

	n := 0
	for _, p := range b.data.Supplemental().ParentLocales.ParentLocale {

		if p.Parent == "root" {
			continue
		}

		sub := strings.Split(p.Parent, "_")
		parent := parentRel{lang: b.langIndex(sub[0])}
		if len(sub) == 2 {

			parent.maxScript = uint8(b.script.index("Latn"))
			parent.toRegion = uint16(b.region.index(sub[1]))
		} else {
			parent.script = uint8(b.script.index(sub[1]))
			parent.maxScript = parent.script
			parent.toRegion = uint16(b.region.index(sub[2]))
		}
		for _, c := range strings.Split(p.Locales, " ") {
			region := b.region.index(c[strings.LastIndex(c, "_")+1:])
			parent.fromRegion = append(parent.fromRegion, uint16(region))
		}
		parents = append(parents, parent)
		n += len(parent.fromRegion)
	}
	b.writeSliceAddSize("parents", n*2, parents)
}

func main() {
	gen.Init()

	gen.Repackage("gen_common.go", "common.go", "language")

	w := gen.NewCodeWriter()
	defer w.WriteGoFile("tables.go", "language")

	fmt.Fprintln(w, `import "golang.org/x/text/internal/tag"`)

	b := newBuilder(w)
	gen.WriteCLDRVersion(w)

	b.parseIndices()
	b.writeType(fromTo{})
	b.writeLanguage()
	b.writeScript()
	b.writeRegion()
	b.writeVariant()

	b.computeRegionGroups()
	b.writeLikelyData()
	b.writeMatchData()
	b.writeRegionInclusionData()
	b.writeParents()
}
