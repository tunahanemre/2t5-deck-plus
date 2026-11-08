export namespace editorapi {
	
	export class ImportPreview {
	    token: string;
	    path?: string;
	    name?: string;
	    dryRun: boolean;
	    files: preset.ImportFile[];
	    risk: preset.ImportRisk;
	    needsConfirm: boolean;
	    dropped: preset.DroppedAssignment[];
	    inherited: preset.InheritedRisk[];
	
	    static createFrom(source: any = {}) {
	        return new ImportPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token = source["token"];
	        this.path = source["path"];
	        this.name = source["name"];
	        this.dryRun = source["dryRun"];
	        this.files = this.convertValues(source["files"], preset.ImportFile);
	        this.risk = this.convertValues(source["risk"], preset.ImportRisk);
	        this.needsConfirm = source["needsConfirm"];
	        this.dropped = this.convertValues(source["dropped"], preset.DroppedAssignment);
	        this.inherited = this.convertValues(source["inherited"], preset.InheritedRisk);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ImportResult {
	    ok: boolean;
	    dryRun: boolean;
	    files: preset.ImportFile[];
	    seq: number;
	    dropped: preset.DroppedAssignment[];
	
	    static createFrom(source: any = {}) {
	        return new ImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.dryRun = source["dryRun"];
	        this.files = this.convertValues(source["files"], preset.ImportFile);
	        this.seq = source["seq"];
	        this.dropped = this.convertValues(source["dropped"], preset.DroppedAssignment);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace engine {
	
	export class Status {
	    Running: boolean;
	    SidecarConnected: boolean;
	    ResolveFrontmost: boolean;
	    Page: string;
	    GateAllow: boolean;
	    ActiveWheel: string;
	    Mode: string;
	    Project: string;
	    LastMIDI: string;
	    AccessibilityOK: boolean;
	    ScreenRecordingOK: boolean;
	    SidecarError: string;
	    SidecarState: string;
	    SidecarStale: boolean;
	    ActiveClip: string;
	    MarkerMode: string;
	    Timecode: string;
	    TimelineName: string;
	    Bank: string;
	    Layer: number;
	    HeldOverlay: string;
	    Target: string;
	    TimelineDuration: string;
	    ClipIn: string;
	    ClipOut: string;
	    ClipDuration: string;
	    ClipTotal: number;
	    ClipRendered: number;
	    MarkerCount: number;
	    LEDs: string;
	    ClipHead: string;
	    ClipTail: string;
	    PMode: string;
	    CustomMode: boolean;
	    FineMode: boolean;
	    Node: number;
	    LastAction: string;
	    EditClipName: string;
	    HaveEditClip: boolean;
	    EditClipPan: number;
	    EditClipTilt: number;
	    EditClipZoom: number;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Running = source["Running"];
	        this.SidecarConnected = source["SidecarConnected"];
	        this.ResolveFrontmost = source["ResolveFrontmost"];
	        this.Page = source["Page"];
	        this.GateAllow = source["GateAllow"];
	        this.ActiveWheel = source["ActiveWheel"];
	        this.Mode = source["Mode"];
	        this.Project = source["Project"];
	        this.LastMIDI = source["LastMIDI"];
	        this.AccessibilityOK = source["AccessibilityOK"];
	        this.ScreenRecordingOK = source["ScreenRecordingOK"];
	        this.SidecarError = source["SidecarError"];
	        this.SidecarState = source["SidecarState"];
	        this.SidecarStale = source["SidecarStale"];
	        this.ActiveClip = source["ActiveClip"];
	        this.MarkerMode = source["MarkerMode"];
	        this.Timecode = source["Timecode"];
	        this.TimelineName = source["TimelineName"];
	        this.Bank = source["Bank"];
	        this.Layer = source["Layer"];
	        this.HeldOverlay = source["HeldOverlay"];
	        this.Target = source["Target"];
	        this.TimelineDuration = source["TimelineDuration"];
	        this.ClipIn = source["ClipIn"];
	        this.ClipOut = source["ClipOut"];
	        this.ClipDuration = source["ClipDuration"];
	        this.ClipTotal = source["ClipTotal"];
	        this.ClipRendered = source["ClipRendered"];
	        this.MarkerCount = source["MarkerCount"];
	        this.LEDs = source["LEDs"];
	        this.ClipHead = source["ClipHead"];
	        this.ClipTail = source["ClipTail"];
	        this.PMode = source["PMode"];
	        this.CustomMode = source["CustomMode"];
	        this.FineMode = source["FineMode"];
	        this.Node = source["Node"];
	        this.LastAction = source["LastAction"];
	        this.EditClipName = source["EditClipName"];
	        this.HaveEditClip = source["HaveEditClip"];
	        this.EditClipPan = source["EditClipPan"];
	        this.EditClipTilt = source["EditClipTilt"];
	        this.EditClipZoom = source["EditClipZoom"];
	    }
	}

}

export namespace main {
	
	export class EditorResponse {
	    status: number;
	    body: string;
	
	    static createFrom(source: any = {}) {
	        return new EditorResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.body = source["body"];
	    }
	}
	export class Perms {
	    Accessibility: boolean;
	    ScreenRecording: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Perms(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Accessibility = source["Accessibility"];
	        this.ScreenRecording = source["ScreenRecording"];
	    }
	}
	export class StartupState {
	    editorAvailable: boolean;
	    armed: boolean;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new StartupState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.editorAvailable = source["editorAvailable"];
	        this.armed = source["armed"];
	        this.reason = source["reason"];
	    }
	}

}

export namespace preset {
	
	export class DroppedAssignment {
	    preset: string;
	    bank: string;
	    control: string;
	    action: string;
	
	    static createFrom(source: any = {}) {
	        return new DroppedAssignment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.preset = source["preset"];
	        this.bank = source["bank"];
	        this.control = source["control"];
	        this.action = source["action"];
	    }
	}
	export class SettingChange {
	    preset: string;
	    key: string;
	    old: any;
	    new: any;
	    weakens: boolean;
	    why?: string;
	
	    static createFrom(source: any = {}) {
	        return new SettingChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.preset = source["preset"];
	        this.key = source["key"];
	        this.old = source["old"];
	        this.new = source["new"];
	        this.weakens = source["weakens"];
	        this.why = source["why"];
	    }
	}
	export class ImportRisk {
	    shell: number;
	    keys: number;
	    destructive: number;
	    unknown: number;
	    panic: number;
	    panicOverrides: number;
	    panicRemoved: number;
	    settings: number;
	
	    static createFrom(source: any = {}) {
	        return new ImportRisk(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.shell = source["shell"];
	        this.keys = source["keys"];
	        this.destructive = source["destructive"];
	        this.unknown = source["unknown"];
	        this.panic = source["panic"];
	        this.panicOverrides = source["panicOverrides"];
	        this.panicRemoved = source["panicRemoved"];
	        this.settings = source["settings"];
	    }
	}
	export class ImportFile {
	    id: string;
	    name: string;
	    status: string;
	    added: number;
	    changed: number;
	    removed: number;
	    risk: ImportRisk;
	    runsProgram: string[];
	    overrides: string[];
	    settingsChanged: SettingChange[];
	    panicRemoved: string[];
	
	    static createFrom(source: any = {}) {
	        return new ImportFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.status = source["status"];
	        this.added = source["added"];
	        this.changed = source["changed"];
	        this.removed = source["removed"];
	        this.risk = this.convertValues(source["risk"], ImportRisk);
	        this.runsProgram = source["runsProgram"];
	        this.overrides = source["overrides"];
	        this.settingsChanged = this.convertValues(source["settingsChanged"], SettingChange);
	        this.panicRemoved = source["panicRemoved"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class InheritedRisk {
	    preset: string;
	    bank: string;
	    control: string;
	    risk: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new InheritedRisk(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.preset = source["preset"];
	        this.bank = source["bank"];
	        this.control = source["control"];
	        this.risk = source["risk"];
	        this.reason = source["reason"];
	    }
	}

}

