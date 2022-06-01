// The goal here is to implement a mini DSL that allows users to have
// granular control over the generated qemu.conf for a VM instance using lxd
// config keys.

// The public function of this file is qemuRawCfgOverride, which takes a
// []cfgSection slice plus an expandedConfig map, and returns a new
// []cfgSection slice representing the "edited" config.

// These are rules for customizing the generated qemu.conf:
//
// - To override configuration entry, the user specifies the new value using a
//   "raw.qemu.config.SECTION.ENTRY" lxd config key, where SECTION is the name
//   of the qemu.conf config section, and ENTRY is the config key under
//   SECTION. For example:
//
//       raw.qemu.config.global.value: 0
//
//   will set:
//
//       [global]
//       value = "0"
//
//   If the value would already have been set in the generated config, then it
//   will be overriden. Since qemu.conf allows multiple config sections with the
//   exact same name, the lxd config can specify which section should be updated
//   by using a bracket syntax like this:
//
//       raw.qemu.config.global[1].value: 1
//
//   In the above example, the second [global] section would be changed (the
//   bracket indexes start at 0). When the user doesn't specify an index, it
//   is assumed to be "0", so the first example is equivalent to:
//
//       raw.qemu.config.global[0].value: 0
//
//   Note that qemu.conf section names can have spaces and double quotes, so
//   it required to specify the section exactly as it appears inside the brackets.
//   For example, to change the gpu driver, one could use:
//
//       raw.qemu.config.device "qemu_gpu".driver: qxl-vga
//
//   Besides adding and editing entries/sections, the user can also delete by
//   an empty string as the value. For example, to remove the `multifunction = "on"`
//   entry from [device "qemu_balloon"], one can use:
//
//       raw.qemu.config.device "qemu_balloon".multifunction: ""
//
//   It is also possible to delete whole sections by specifying an empty string
//   as the value without specifying the entry name. So, to delete the whole
//   `device "qemu_balloon"` section, one could do:
//
//       raw.qemu.config.device "qemu_balloon": ""
//
//   One last thing to note is that section indexes (the bracket number after
//   section name) are also used when appending new sections, but they don't
//   have to follow a strict sequence. So if one specifies:
//
//       raw.qemu.config.global[11].value: "10"
//       raw.qemu.config.global[2].value: "1"
//
//   Then two new [global] sections will be appended to the end of the qemu.conf
//   (assuming only 2 existed before), with the second one having the value "10".
package drivers

import (
	"regexp"
	"sort"
	"strconv"
)

const pattern = `^raw\.qemu\.config\.([^.\[]+)(?:\[(\d+)\])?(?:\.(.+))?$`

var rawConfigPattern = regexp.MustCompile(pattern)

type rawConfigKey struct {
	sectionName string
	index       uint
	entryKey    string
}

type configMap map[rawConfigKey]string

func sortedConfigKeys(cfgMap configMap) []rawConfigKey {
	rv := []rawConfigKey{}

	for k := range cfgMap {
		rv = append(rv, k)
	}

	sort.Slice(rv, func(i, j int) bool {
		return rv[i].sectionName < rv[j].sectionName ||
			rv[i].index < rv[j].index ||
			rv[i].entryKey < rv[j].entryKey
	})

	return rv
}

// Extracts all raw.qemu.config.* keys into a separate map. It also normalizes
// all sections to have an explicit index, so that keys like
// "raw.config.qemu.section.entry" become "raw.config.qemu.section[0].entry"
func extractRawConfigKeys(expandedConfig map[string]string) configMap {
	rv := configMap{}

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
			if err != nil || i < 0 {
				panic("failed to parse index")
			}
			k.index = uint(i)
		}

		rv[k] = value
	}

	return rv
}

func updateEntries(entries []cfgEntry, sk rawConfigKey, cfgMap configMap) []cfgEntry {
	rv := []cfgEntry{}

	for _, entry := range entries {

		newEntry := cfgEntry{
			key:   entry.key,
			value: entry.value,
		}

		ek := rawConfigKey{sk.sectionName, sk.index, entry.key}
		if val, ok := cfgMap[ek]; ok {
			// override
			delete(cfgMap, ek)
			newEntry.value = val
		}

		rv = append(rv, newEntry)
	}

	return rv
}

func appendEntries(entries []cfgEntry, sk rawConfigKey, cfgMap configMap) []cfgEntry {
	// sort to have deterministic output in the appended entries
	sortedKeys := sortedConfigKeys(cfgMap)
	// processed all modifications for the current section, now
	// handle new entries
	for _, rawKey := range sortedKeys {
		if rawKey.sectionName != sk.sectionName || rawKey.index != sk.index {
			continue
		}

		newEntry := cfgEntry{
			key:   rawKey.entryKey,
			value: cfgMap[rawKey],
		}

		entries = append(entries, newEntry)
		delete(cfgMap, rawKey)
	}

	return entries
}

func updateSections(cfg []cfgSection, cfgMap configMap) []cfgSection {
	newCfg := []cfgSection{}
	sectionCounts := map[string]uint{}

	for _, section := range cfg {
		count, ok := sectionCounts[section.name]

		if ok {
			sectionCounts[section.name] = count + 1
		} else {
			sectionCounts[section.name] = 1
		}

		index := sectionCounts[section.name] - 1
		sk := rawConfigKey{section.name, index, ""}

		if val, ok := cfgMap[sk]; ok {
			if val == "" {
				// deleted section
				delete(cfgMap, sk)
				continue
			}
		}

		newSection := cfgSection{
			name:    section.name,
			comment: section.comment,
		}

		newSection.entries = updateEntries(section.entries, sk, cfgMap)
		newSection.entries = appendEntries(newSection.entries, sk, cfgMap)

		newCfg = append(newCfg, newSection)
	}

	return newCfg
}

func appendSections(newCfg []cfgSection, cfgMap configMap) []cfgSection {
	tmp := map[rawConfigKey]cfgSection{}
	// sort to have deterministic output in the appended entries
	sortedKeys := sortedConfigKeys(cfgMap)

	for _, k := range sortedKeys {
		if k.entryKey == "" {
			// makes no sense to process section deletions (the only case where
			// entryKey == "") since we are only adding new sections now
			continue
		}
		sectionKey := rawConfigKey{k.sectionName, k.index, ""}
		section, found := tmp[sectionKey]
		if !found {
			section = cfgSection{
				name: k.sectionName,
			}
		}
		section.entries = append(section.entries, cfgEntry{
			key:   k.entryKey,
			value: cfgMap[k],
		})
		tmp[sectionKey] = section
	}

	rawSections := []rawConfigKey{}
	for rawSection := range tmp {
		rawSections = append(rawSections, rawSection)
	}

	// Sort to have deterministic output in the appended sections
	sort.Slice(rawSections, func(i, j int) bool {
		return rawSections[i].sectionName < rawSections[j].sectionName ||
			rawSections[i].index < rawSections[j].index
	})

	for _, rawSection := range rawSections {
		newCfg = append(newCfg, tmp[rawSection])
	}

	return newCfg
}

func qemuRawCfgOverride(cfg []cfgSection, expandedConfig map[string]string) []cfgSection {
	cfgMap := extractRawConfigKeys(expandedConfig)

	if len(cfgMap) == 0 {
		// If no keys are found, we return the cfg unmodified.
		return cfg
	}

	newCfg := updateSections(cfg, cfgMap)
	newCfg = appendSections(newCfg, cfgMap)

	return newCfg
}
