package cn.yucg.yudesk;

/** Do not label the physical Home key as the desktop's home screen. */
final class RemoteActions {
    static final class Key {
        final String type,key,code;
        Key(String type,String key,String code){this.type=type;this.key=key;this.code=code;}
    }
    static String backLabel(String platform){return "windows".equals(platform)?"后退":"android".equals(platform)?"返回":"Esc";}
    static String homeLabel(String platform){return "windows".equals(platform)?"桌面":"android".equals(platform)?"主页":"Home 键";}
    static Key[] back(String platform){return "windows".equals(platform)?chord("Alt","AltLeft","ArrowLeft","ArrowLeft"):tap("Escape","Escape");}
    static Key[] home(String platform){return "windows".equals(platform)?chord("Meta","MetaLeft","d","KeyD"):tap("Home","Home");}
    static Key[] tap(String key,String code){return new Key[]{new Key("key_down",key,code),new Key("key_up",key,code)};}
    static Key[] chord(String modifier,String modifierCode,String key,String code){return new Key[]{new Key("key_down",modifier,modifierCode),new Key("key_down",key,code),new Key("key_up",key,code),new Key("key_up",modifier,modifierCode)};}
}
