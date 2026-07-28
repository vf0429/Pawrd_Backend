package raggo

import (
	"bufio"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

type Chunk struct {
	Text     string
	Metadata map[string]string
}

type structuredPlanLimit struct {
	product string
	limit   string
	remark  string
}

type structuredWaitingPeriod struct {
	product string
	text    string
}

var (
	headingRe  = regexp.MustCompile(`^(#{1,6})\s+(.*\S)\s*$`)
	anchorRe   = regexp.MustCompile(`^>\s*([A-Za-z][A-Za-z ]*):\s*(.*?)\s*$`)
	chunkCache = struct {
		mu    sync.RWMutex
		items map[string][]Chunk
	}{
		items: map[string][]Chunk{},
	}
)

func LoadChunks(cfg Config) ([]Chunk, error) {
	cacheKey := cfg.DataPath
	chunkCache.mu.RLock()
	if cached, ok := chunkCache.items[cacheKey]; ok {
		out := make([]Chunk, len(cached))
		copy(out, cached)
		chunkCache.mu.RUnlock()
		return out, nil
	}
	chunkCache.mu.RUnlock()

	paths, err := markdownFiles(cfg.DataPath)
	if err != nil {
		return nil, err
	}
	chunks := make([]Chunk, 0, 512)
	for _, path := range paths {
		fileChunks, err := loadFileChunks(cfg, path)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, fileChunks...)
	}
	structuredChunks, err := loadStructuredInsuranceChunks(cfg)
	if err != nil {
		return nil, err
	}
	chunks = append(chunks, structuredChunks...)

	cached := make([]Chunk, len(chunks))
	copy(cached, chunks)
	chunkCache.mu.Lock()
	chunkCache.items[cacheKey] = cached
	chunkCache.mu.Unlock()

	return chunks, nil
}

func loadStructuredInsuranceChunks(cfg Config) ([]Chunk, error) {
	dbPath := filepath.Join(detectProjectRoot(), "assets", "pet_insurance.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	providerByID := map[int]string{}
	rows, err := db.Query(`SELECT company_id, company_name FROM insurance_provider`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			providerByID[id] = normalizeProviderName(name)
		}
	}
	rows.Close()

	chunks := make([]Chunk, 0, 256)
	waitingPeriodGroups := map[string][]structuredWaitingPeriod{}
	limitGroups := map[string][]structuredPlanLimit{}

	productRows, err := db.Query(`
		SELECT insurance_id, provider_id, insurance_name, insurance_name_zh, waiting_period, waiting_period_zh
		FROM product
	`)
	if err != nil {
		return nil, err
	}
	for productRows.Next() {
		var insuranceID, providerID int
		var insuranceName, insuranceNameZh string
		var waitingPeriod, waitingPeriodZh sql.NullString
		if err := productRows.Scan(&insuranceID, &providerID, &insuranceName, &insuranceNameZh, &waitingPeriod, &waitingPeriodZh); err != nil {
			continue
		}
		provider := providerByID[providerID]
		if !contains(SupportedProviders, provider) {
			continue
		}
		if strings.TrimSpace(waitingPeriod.String) != "" {
			waitingPeriodGroups[provider+"|en"] = append(waitingPeriodGroups[provider+"|en"], structuredWaitingPeriod{
				product: insuranceName,
				text:    strings.TrimSpace(waitingPeriod.String),
			})
			chunks = append(chunks, buildStructuredChunk(
				provider, "en", insuranceName, "structured_product_waiting_period",
				"Structured Product Data > Waiting Period",
				strconv.Itoa(insuranceID),
				"waiting_period",
				"structured_product,waiting_period",
				insuranceName+": waiting period "+strings.TrimSpace(waitingPeriod.String),
			))
		}
		if strings.TrimSpace(waitingPeriodZh.String) != "" {
			productTitle := insuranceNameZh
			if strings.TrimSpace(productTitle) == "" {
				productTitle = insuranceName
			}
			waitingPeriodGroups[provider+"|zh"] = append(waitingPeriodGroups[provider+"|zh"], structuredWaitingPeriod{
				product: productTitle,
				text:    strings.TrimSpace(waitingPeriodZh.String),
			})
			chunks = append(chunks, buildStructuredChunk(
				provider, "zh", productTitle, "structured_product_waiting_period",
				"Structured Product Data > 等候期",
				strconv.Itoa(insuranceID),
				"waiting_period",
				"structured_product,waiting_period",
				productTitle+"：等候期 "+strings.TrimSpace(waitingPeriodZh.String),
			))
		}
	}
	productRows.Close()

	limitRows, err := db.Query(`
		SELECT p.insurance_id, p.insurance_name, p.insurance_name_zh, p.provider_id,
		       s.parent_coverage_id, s.sub_coverage_name, s.sub_coverage_name_zh,
		       s.sub_limit, s.sub_coverage_remark, s.sub_coverage_remark_zh
		FROM sub_coverage_limit s
		JOIN product p ON p.insurance_id = s.product_id
	`)
	if err != nil {
		return nil, err
	}
	for limitRows.Next() {
		var insuranceID, providerID, parentCoverageID int
		var insuranceName, insuranceNameZh string
		var subName, subNameZh, subLimit string
		var remark, remarkZh sql.NullString
		if err := limitRows.Scan(&insuranceID, &insuranceName, &insuranceNameZh, &providerID, &parentCoverageID, &subName, &subNameZh, &subLimit, &remark, &remarkZh); err != nil {
			continue
		}
		provider := providerByID[providerID]
		if !contains(SupportedProviders, provider) {
			continue
		}
		if strings.TrimSpace(subName) != "" {
			text := buildStructuredLimitText(insuranceName, subName, subLimit, remark.String)
			limitGroups[provider+"|en|"+strings.ToLower(strings.TrimSpace(subName))] = append(limitGroups[provider+"|en|"+strings.ToLower(strings.TrimSpace(subName))], structuredPlanLimit{
				product: insuranceName,
				limit:   strings.TrimSpace(subLimit),
				remark:  sanitizeStructuredRemark(subName, remark.String),
			})
			chunks = append(chunks, buildStructuredChunk(
				provider, "en", insuranceName, "structured_sub_coverage_limit",
				"Structured Product Data > Coverage Limits > "+subName,
				strconv.Itoa(parentCoverageID),
				"benefit",
				"structured_product,limit,benefit",
				text,
			))
		}
		if strings.TrimSpace(subNameZh) != "" {
			productTitle := insuranceNameZh
			if strings.TrimSpace(productTitle) == "" {
				productTitle = insuranceName
			}
			text := buildStructuredLimitText(productTitle, subNameZh, subLimit, remarkZh.String)
			limitGroups[provider+"|zh|"+strings.TrimSpace(subNameZh)] = append(limitGroups[provider+"|zh|"+strings.TrimSpace(subNameZh)], structuredPlanLimit{
				product: productTitle,
				limit:   strings.TrimSpace(subLimit),
				remark:  sanitizeStructuredRemark(subNameZh, remarkZh.String),
			})
			chunks = append(chunks, buildStructuredChunk(
				provider, "zh", productTitle, "structured_sub_coverage_limit",
				"Structured Product Data > 保障限額 > "+subNameZh,
				strconv.Itoa(parentCoverageID),
				"benefit",
				"structured_product,limit,benefit",
				text,
			))
		}
	}
	limitRows.Close()

	return chunks, nil
}

func buildStructuredChunk(provider, language, product, sourceName, sectionPath, clause, unitType, topicTags, text string) Chunk {
	return Chunk{
		Text: strings.TrimSpace(text),
		Metadata: map[string]string{
			"provider":     provider,
			"source_name":  sourceName,
			"source_path":  "assets/pet_insurance.db",
			"language":     language,
			"product":      product,
			"policy_type":  "pet_insurance",
			"section_path": sectionPath,
			"clauses":      clause,
			"unit_types":   unitType,
			"topic_tags":   topicTags,
		},
	}
}

func buildStructuredLimitText(product, subName, subLimit, remark string) string {
	parts := []string{strings.TrimSpace(product), strings.TrimSpace(subName)}
	if strings.TrimSpace(subLimit) != "" {
		parts = append(parts, "limit "+strings.TrimSpace(subLimit))
	}
	if cleanedRemark := sanitizeStructuredRemark(subName, remark); cleanedRemark != "" {
		parts = append(parts, cleanedRemark)
	}
	return strings.Join(parts, ": ")
}

func allWaitingTextsEqual(items []structuredWaitingPeriod) bool {
	if len(items) < 2 {
		return true
	}
	first := normalizeOptionalString(items[0].text)
	for _, item := range items[1:] {
		if normalizeOptionalString(item.text) != first {
			return false
		}
	}
	return true
}

func joinProductNames(items []structuredWaitingPeriod) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.product)
	}
	return strings.Join(names, ", ")
}

func sanitizeStructuredRemark(subName, remark string) string {
	remark = strings.Join(strings.Fields(strings.TrimSpace(remark)), " ")
	if remark == "" {
		return ""
	}

	nameLower := strings.ToLower(strings.TrimSpace(subName))
	remarkLower := strings.ToLower(remark)

	switch {
	case strings.Contains(nameLower, "veterinary consultation"):
		if strings.Contains(remarkLower, "confinement of no less than 12 consecutive hours") {
			return ""
		}
	case strings.Contains(nameLower, "room and board"):
		if strings.Contains(remarkLower, "consultation fees") || strings.Contains(remarkLower, "max. 20 visits") {
			return ""
		}
	}

	return remark
}

func normalizeProviderName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(lower, "one degree"):
		return "one_degree"
	case strings.Contains(lower, "blue cross"):
		return "bluecross"
	case strings.Contains(lower, "prudential"):
		return "prudential"
	case strings.Contains(lower, "msig"):
		return "msig"
	default:
		return strings.ReplaceAll(lower, " ", "_")
	}
}

func markdownFiles(root string) ([]string, error) {
	found := make([]string, 0, 16)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, ".") || name == "README.md" {
			return nil
		}
		if strings.EqualFold(filepath.Ext(name), ".md") {
			found = append(found, path)
		}
		return nil
	})
	sort.Strings(found)
	return found, err
}

func loadFileChunks(cfg Config, path string) ([]Chunk, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	frontmatter, body := parseFrontmatter(text)

	provider := normalizeOptionalString(frontmatter["provider"])
	if provider == "" {
		provider = normalizeOptionalString(filepath.Base(filepath.Dir(path)))
	}
	if !contains(SupportedProviders, provider) {
		return nil, nil
	}

	language := normalizeOptionalString(frontmatter["language"])
	if language == "" {
		language = detectLanguage(path, body)
	}

	rel, _ := filepath.Rel(cfg.DataPath, path)
	baseMeta := map[string]string{
		"provider":             provider,
		"source_name":          filepath.Base(path),
		"source_path":          rel,
		"language":             language,
		"product":              frontmatter["product"],
		"policy_type":          frontmatter["policy_type"],
		"source_file":          frontmatter["source_file"],
		"source_version_label": frontmatter["source_version_label"],
		"normalization_status": frontmatter["normalization_status"],
		"schema_version":       frontmatter["schema_version"],
		"chunker_version":      ChunkerVersion,
	}

	return chunkMarkdownBody(body, baseMeta), nil
}

func parseFrontmatter(raw string) (map[string]string, string) {
	meta := make(map[string]string)
	if !strings.HasPrefix(raw, "---\n") {
		return meta, raw
	}
	rest := strings.TrimPrefix(raw, "---\n")
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return meta, raw
	}
	fm := rest[:idx]
	body := rest[idx+len("\n---\n"):]
	sc := bufio.NewScanner(strings.NewReader(fm))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key != "" && val != "" && !strings.HasPrefix(val, "{") && !strings.HasPrefix(val, "[") {
			meta[key] = val
		}
	}
	return meta, body
}

func detectLanguage(path, body string) string {
	lower := strings.ToLower(filepath.Base(path))
	if strings.Contains(lower, "_zh") || strings.Contains(lower, "-zh") || strings.Contains(lower, ".zh") {
		return "zh"
	}
	if strings.Contains(lower, "_en") || strings.Contains(lower, "-en") || strings.Contains(lower, ".en") {
		return "en"
	}
	han := 0
	total := 0
	for _, r := range body {
		if r <= ' ' {
			continue
		}
		total++
		if r >= 0x4E00 && r <= 0x9FFF {
			han++
		}
	}
	if total == 0 {
		return "en"
	}
	if float64(han)/float64(total) >= 0.05 {
		return "zh"
	}
	return "en"
}

func chunkMarkdownBody(body string, baseMeta map[string]string) []Chunk {
	lines := strings.Split(body, "\n")
	headingPath := make([]string, 0, 6)
	curAnchor := make(map[string]string)
	sectionBuf := make([]string, 0, 32)
	chunks := make([]Chunk, 0, 64)

	flush := func() {
		text := strings.TrimSpace(strings.Join(sectionBuf, "\n"))
		sectionBuf = sectionBuf[:0]
		if text == "" {
			return
		}
		parts := splitByBudget(text, 1200)
		for i, part := range parts {
			m := copyMeta(baseMeta)
			m["chunk_index"] = strconv.Itoa(i)
			m["section_path"] = strings.Join(headingPath, " > ")
			m["clauses"] = curAnchor["clause"]
			m["unit_types"] = normalizeOptionalString(curAnchor["unit"])
			m["topic_tags"] = inferTopicTags(part, m["unit_types"], m["section_path"])
			chunks = append(chunks, Chunk{
				Text:     part,
				Metadata: m,
			})
		}
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush()
			depth := len(m[1])
			title := strings.TrimSpace(m[2])
			if depth <= 0 {
				continue
			}
			if depth > len(headingPath) {
				headingPath = append(headingPath, title)
			} else {
				headingPath = append(headingPath[:depth-1], title)
			}
			curAnchor = make(map[string]string)
			continue
		}
		if m := anchorRe.FindStringSubmatch(line); m != nil {
			key := strings.ToLower(strings.TrimSpace(m[1]))
			val := strings.TrimSpace(m[2])
			curAnchor[key] = val
		}
		sectionBuf = append(sectionBuf, line)
	}
	flush()
	return chunks
}

func inferTopicTags(text, unitType, sectionPath string) string {
	l := strings.ToLower(text)
	sectionLower := strings.ToLower(sectionPath)
	tags := make([]string, 0, 4)
	if unitType != "" {
		tags = append(tags, unitType)
	}
	if isWaitingPeriodSection(sectionLower) || ((strings.Contains(l, "waiting period") || strings.Contains(l, "等候期")) && unitType == "waiting_period") {
		tags = append(tags, "waiting_period")
	}
	if strings.Contains(sectionLower, "consult") || strings.Contains(sectionLower, "診症") || strings.Contains(l, "獸醫診症") {
		tags = append(tags, "consult")
	}
	if strings.Contains(l, "plan a") || strings.Contains(l, "plan b") || strings.Contains(l, "hk$") {
		tags = append(tags, "limit")
	}
	uniq := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		uniq = append(uniq, t)
	}
	return strings.Join(uniq, ", ")
}

func isWaitingPeriodSection(sectionLower string) bool {
	return strings.Contains(sectionLower, "definition: waiting period") ||
		strings.Contains(sectionLower, "定義：等候期") ||
		strings.Contains(sectionLower, "waiting periods") ||
		strings.Contains(sectionLower, "等候期")
}

func splitByBudget(text string, maxChars int) []string {
	if len(text) <= maxChars {
		return []string{text}
	}
	paras := strings.Split(text, "\n\n")
	out := make([]string, 0, 4)
	buf := strings.Builder{}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		next := p
		if buf.Len() > 0 {
			next = buf.String() + "\n\n" + p
		}
		if len(next) <= maxChars {
			buf.Reset()
			buf.WriteString(next)
			continue
		}
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
		if len(p) <= maxChars {
			buf.WriteString(p)
			continue
		}
		// Fallback hard split for very long paragraphs.
		start := 0
		for start < len(p) {
			end := start + maxChars
			if end > len(p) {
				end = len(p)
			}
			out = append(out, strings.TrimSpace(p[start:end]))
			start = end
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func copyMeta(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
