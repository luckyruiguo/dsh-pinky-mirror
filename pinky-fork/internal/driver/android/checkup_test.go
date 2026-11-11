package android

import "testing"

func TestParseProps(t *testing.T) {
	out := "[ro.product.brand]: [Redmi]\r\n[ro.mi.os.version.name]: [OS2.0]\n[empty]: []\n[gsm.operator.alpha]: [CMCC,]\nnot a property\n"
	props := parseProps(out)
	if props["ro.product.brand"] != "Redmi" || props["ro.mi.os.version.name"] != "OS2.0" || props["gsm.operator.alpha"] != "CMCC," {
		t.Errorf("props = %q", props)
	}
	if v, ok := props["empty"]; !ok || v != "" {
		t.Errorf("an empty property: %q, %v", v, ok)
	}
	if len(props) != 4 {
		t.Errorf("%d properties: %q", len(props), props)
	}
}

func TestFamilyOf(t *testing.T) {
	for _, c := range []struct {
		name           string
		props          map[string]string
		family, system string
	}{
		{"HyperOS 2", map[string]string{"ro.product.manufacturer": "Xiaomi", "ro.product.brand": "Redmi",
			"ro.mi.os.version.name": "OS2.0", "ro.miui.ui.version.name": "V816"}, "xiaomi", "HyperOS 2.0"},
		{"MIUI 14", map[string]string{"ro.product.manufacturer": "Xiaomi", "ro.miui.ui.version.name": "V140"}, "xiaomi", "MIUI 14"},
		{"MIUI 12.5", map[string]string{"ro.product.manufacturer": "Xiaomi", "ro.miui.ui.version.name": "V125"}, "xiaomi", "MIUI 12.5"},
		{"LineageOS on a Mi 10 Pro", map[string]string{"ro.product.manufacturer": "Xiaomi", "ro.product.brand": "Xiaomi",
			"ro.product.model": "Mi 10 Pro", "ro.lineage.build.version": "23.0"}, "", "LineageOS 23.0"},
		{"ColorOS", map[string]string{"ro.product.manufacturer": "OPPO", "ro.product.brand": "OPPO", "ro.build.version.oplusrom": "V15.0.0"}, "oppo", "ColorOS 15.0.0"},
		{"older ColorOS", map[string]string{"ro.product.manufacturer": "OPPO", "ro.build.version.opporom": "V7.1"}, "oppo", "ColorOS 7.1"},
		{"OxygenOS", map[string]string{"ro.product.manufacturer": "OnePlus", "ro.product.brand": "OnePlus", "ro.build.version.oplusrom": "V15.0.0"}, "oppo", "OxygenOS 15.0.0"},
		{"realme UI", map[string]string{"ro.product.manufacturer": "realme", "ro.product.brand": "realme",
			"ro.build.version.oplusrom": "V14.0", "ro.build.version.realmeui": "V5.0"}, "oppo", "realme UI 5.0"},
		{"OriginOS", map[string]string{"ro.product.manufacturer": "vivo", "ro.product.brand": "iQOO", "ro.vivo.os.name": "Funtouch",
			"ro.vivo.os.version": "15.0", "ro.vivo.os.build.display.id": "OriginOS 5"}, "vivo", "OriginOS 5"},
		{"Funtouch OS", map[string]string{"ro.product.manufacturer": "vivo", "ro.vivo.os.name": "Funtouch", "ro.vivo.os.build.display.id": "Funtouch OS_14"}, "vivo", "Funtouch OS 14"},
		{"vivo with an unknown display id", map[string]string{"ro.product.manufacturer": "vivo", "ro.vivo.os.build.display.id": "PD2309"}, "vivo", ""},
		{"LineageOS on a vivo", map[string]string{"ro.product.manufacturer": "vivo", "ro.lineage.build.version": "22.2"}, "", "LineageOS 22.2"},
		{"MagicOS", map[string]string{"ro.product.manufacturer": "HONOR", "ro.build.version.magic": "MagicOS_9.0.0", "msc.config.magic.version": "9.0"}, "honor", "MagicOS 9.0.0"},
		{"Magic UI 6", map[string]string{"ro.product.manufacturer": "HONOR", "ro.build.version.magic": "MagicUI_6.1.0"}, "honor", "Magic UI 6.1.0"},
		{"HONOR made by Huawei", map[string]string{"ro.product.manufacturer": "HUAWEI", "ro.product.brand": "HONOR",
			"ro.build.version.magic": "3.0.1", "ro.build.version.emui": "EmotionUI_10.0.1"}, "honor", "Magic UI 3.0.1"},
		{"HarmonyOS 4.2", map[string]string{"ro.product.manufacturer": "HUAWEI", "ro.build.version.emui": "EmotionUI_14.2.0",
			"hw_sc.build.platform.version": "4.2.0", "ro.huawei.build.display.id": "LIO-AL00 4.2.0.120(SP1C00E100R5P4)"}, "huawei", "HarmonyOS 4.2.0"},
		{"EMUI 15 overseas", map[string]string{"ro.product.manufacturer": "HUAWEI", "ro.build.version.emui": "EmotionUI_15.0.0",
			"hw_sc.build.platform.version": "4.3.0", "ro.huawei.build.display.id": "ICL-L29 15.0.0.209(C636E3R1P1)"}, "huawei", "EMUI 15.0.0"},
		{"EMUI", map[string]string{"ro.product.manufacturer": "HUAWEI", "ro.build.version.emui": "EmotionUI_12.0.0"}, "huawei", "EMUI 12.0.0"},
		{"OnePlus in China", map[string]string{"ro.product.manufacturer": "OnePlus", "ro.product.brand": "OnePlus",
			"ro.build.version.oplusrom": "V15.0.2", "ro.oplus.image.system_ext.area": "domestic"}, "oppo", "ColorOS 15.0.2"},
		{"One UI 6.1", map[string]string{"ro.product.manufacturer": "samsung", "ro.build.version.oneui": "60100"}, "samsung", "One UI 6.1"},
		{"Samsung without the version", map[string]string{"ro.product.manufacturer": "samsung"}, "samsung", "One UI"},
		{"Pixel", map[string]string{"ro.product.manufacturer": "Google", "ro.product.model": "Pixel 9"}, "pixel", ""},
		{"emulator", map[string]string{"ro.product.manufacturer": "Google", "ro.product.model": "sdk_gphone64_arm64", "ro.kernel.qemu": "1"}, "", ""},
		{"Motorola", map[string]string{"ro.product.manufacturer": "motorola", "ro.product.model": "moto g"}, "", ""},
	} {
		family, system := familyOf(c.props)
		if family != c.family || system != c.system {
			t.Errorf("%s: %q, %q; want %q, %q", c.name, family, system, c.family, c.system)
		}
	}
}

func TestVersions(t *testing.T) {
	for in, want := range map[string]string{"V125": "12.5", "V130": "13", "V140": "14", "V12": "12", "v11": "11", "Vx": ""} {
		if got := miuiVersion(in); got != want {
			t.Errorf("miuiVersion(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"60100": "6.1", "80000": "8.0", "50101": "5.1", "x": "", "12": ""} {
		if got := oneUIVersion(in); got != want {
			t.Errorf("oneUIVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInputRefused(t *testing.T) {
	for line, want := range map[string]bool{
		"java.lang.SecurityException: Injecting input events requires the caller (or the source of the instrumentation, if any) to have the INJECT_EVENTS permission.": true,
		"[server] ERROR: java.lang.SecurityException: Injecting to another application requires INJECT_EVENTS permission":                                              true,
		"[server] INFO: Device: [Xiaomi] Redmi 23013RK75C (Android 14)":                                                                                                false,
	} {
		if got := inputRefused(line); got != want {
			t.Errorf("inputRefused(%q) = %v", line, got)
		}
	}
	if got := serverMessage("[server] ERROR: java.lang.SecurityException: no"); got != "java.lang.SecurityException: no" {
		t.Errorf("serverMessage = %q", got)
	}
}

func TestXiaomiInputSwitch(t *testing.T) {
	// The switch's property, as getprop prints it on a phone that has it
	// on but was not restarted since.
	props := parseProps("[persist.security.adbinput]: [1]\n[ro.mi.os.version.name]: [OS3.0]\n")
	if props["persist.security.adbinput"] != "1" {
		t.Fatalf("props = %q", props)
	}
	if f, _ := familyOf(props); f != "xiaomi" {
		t.Errorf("family = %q", f)
	}
}
