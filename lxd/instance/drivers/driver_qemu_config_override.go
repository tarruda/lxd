package drivers

import (
	"regexp"
	"sort"
	"strconv"
)

var rawConfigPattern = regexp.MustCompile(`^raw\.qemu\.config\.([^.\[]+)(?:\[(\d)\])?(?:\.(.+))?$`)

type rawConfigKey struct {
	sectionName string
	index       uint
	entryKey    string
}

func sortedConfigKeys(cfgMap map[rawConfigKey]string) []rawConfigKey {
	rv := []rawConfigKey{}

	for k := range cfgMap {
		rv = append(rv, k)
	}

	sort.Slice(rv, func(i, j int) bool {
		return rv[i].sectionName < rv[j].sectionName ||
			rv[i].entryKey < rv[j].entryKey ||
			rv[i].index < rv[j].index
	})

	return rv
}

// Extracts all raw.qemu.config.* keys into a separate map. It also normalizes
// all sections to have an explicit index, so that keys like
// "raw.config.qemu.section.entry" become "raw.config.qemu.section[0].entry"
func qemuExtractRawConfigKeys(expandedConfig map[string]string) map[rawConfigKey]string {
	rv := map[rawConfigKey]string{}

	for rawKey, value := range expandedConfig {
		matches := rawConfigPattern.FindStringSubmatch(rawKey)

		if len(matches) == 0 {
			// ignore keys that don't match the pattern
			continue
		}

		k := rawConfigKey{
			sectionName: matches[1],
			// default index is 0
			index:    0,
			entryKey: matches[3],
		}

		if matches[2] != "" {
			i, err := strconv.Atoi(matches[2])
			if err != nil || i > 9 || i < 0 {
				panic("unexpected failure in index parsing")
			}
			k.index = uint(i)
		}

		rv[k] = value
	}

	return rv
}

func qemuRawCfgOverride(cfg []cfgSection, expandedConfig map[string]string) []cfgSection {
	tmp := qemuExtractRawConfigKeys(expandedConfig)

	if len(tmp) == 0 {
		// If no keys are found, we return the cfg unmodified.
		return cfg
	}

	newCfg := []cfgSection{}
	sectionNameCountMap := map[string]uint{}

	for _, section := range cfg {
		count, ok := sectionNameCountMap[section.name]

		if ok {
			sectionNameCountMap[section.name] = count + 1
		} else {
			sectionNameCountMap[section.name] = 1
		}

		index := sectionNameCountMap[section.name] - 1
		sk := rawConfigKey{section.name, index, ""}

		if val, ok := tmp[sk]; ok {
			if val == "" {
				// user explicitly deleted section
				delete(tmp, sk)
				continue
			}
		}

		// make a copy of the section
		newSection := cfgSection{
			name:    section.name,
			comment: section.comment,
		}

		for _, entry := range section.entries {

			newEntry := cfgEntry{
				key:   entry.key,
				value: entry.value,
			}

			ek := rawConfigKey{section.name, index, entry.key}
			if val, ok := tmp[ek]; ok {
				// override
				delete(tmp, ek)
				newEntry.value = val
			}

			newSection.entries = append(newSection.entries, newEntry)
		}

		// processed all modifications for the current section, now
		// handle new entries
		for rawKey, rawValue := range tmp {
			if rawKey.sectionName != sk.sectionName || rawKey.index != sk.index {
				continue
			}

			newEntry := cfgEntry{
				key:   rawKey.entryKey,
				value: rawValue,
			}

			newSection.entries = append(newSection.entries, newEntry)
			delete(tmp, rawKey)
		}

		newCfg = append(newCfg, newSection)
	}

	sectionMap := map[rawConfigKey]cfgSection{}

	// sort to have deterministic output (can't rely on map
	// iteration order)
	sortedKeys := sortedConfigKeys(tmp)

	// now process new sections
	for _, k := range sortedKeys {
		if k.entryKey == "" {
			continue
		}
		sectionKey := rawConfigKey{k.sectionName, k.index, ""}
		section, found := sectionMap[sectionKey]
		if !found {
			section = cfgSection{
				name: k.sectionName,
			}
		}
		section.entries = append(section.entries, cfgEntry{
			key:   k.entryKey,
			value: tmp[k],
		})
		sectionMap[sectionKey] = section
	}

	rawSections := []rawConfigKey{}
	for rawSection := range sectionMap {
		rawSections = append(rawSections, rawSection)
	}

	// sort to have deterministic output (can't rely on map
	// iteration order)
	sort.Slice(rawSections, func(i, j int) bool {
		return rawSections[i].sectionName < rawSections[j].sectionName ||
			rawSections[i].index < rawSections[j].index
	})

	for _, rawSection := range rawSections {
		newCfg = append(newCfg, sectionMap[rawSection])
	}

	return newCfg
}
