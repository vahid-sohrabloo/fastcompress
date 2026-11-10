package deflatetest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
)

func caseHash(c Case) string {
	h := sha256.Sum256(c.Data)
	return hex.EncodeToString(h[:])
}

// allHash is a sha256 over name, size and content hash of every case.
func allHash(cs []Case) string {
	h := sha256.New()
	for _, c := range cs {
		fmt.Fprintf(h, "%s %d %s\n", c.Name, len(c.Data), caseHash(c))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestDeterminism(t *testing.T) {
	a, b := Cases(true), Cases(true)
	if len(a) != len(b) {
		t.Fatalf("case counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Name != b[i].Name || !bytes.Equal(a[i].Data, b[i].Data) {
			t.Fatalf("case %q differs between calls", a[i].Name)
		}
	}
}

func TestUniqueNames(t *testing.T) {
	for _, short := range []bool{true, false} {
		if !short && testing.Short() {
			continue
		}
		seen := map[string]bool{}
		for _, c := range Cases(short) {
			if seen[c.Name] {
				t.Errorf("duplicate case name %q (short=%v)", c.Name, short)
			}
			seen[c.Name] = true
		}
	}
}

// goldenShort is the sha256 of Cases(true) content for the non-sweep cases;
// goldenAll covers every case (the sweep-* cases only through it). Regenerate with DEFLATETEST_PRINT=1 and only
// when a generator change is intended.
var goldenShort = map[string]string{
	"size-text/510":         "7b41b4dcc524e1300e164c7c99e31bec71cbd4b579166c0da84280030175f50b",
	"size-text/511":         "5a3f06dba72ad726a744c702689dc13e06fca05756dbac2b44076499c1655559",
	"size-text/512":         "1f0089d70e862720f9bd07b6eca0339e95da412196e6cdf01ccf4e1c12d2fe84",
	"size-text/513":         "2bdb753f59bdc8f540d13909b2821ebd4a3e0484572a5381ef4f52cca9e80fcf",
	"size-text/4095":        "032b5e9f412021f9d0077809b73c13c95be580895e4f270c57b3681fb984f07c",
	"size-text/4096":        "216a073f65d128f5a493cde12ad119c12a5c18e3618256a0f86f63d0ac35f46f",
	"size-text/4097":        "fa8790dae7d7b1a7abb7dba94f407fad75635466e3e1f9fc7bc4ce4582e7985f",
	"size-text/4999":        "74fd5aab5758b6254b2e73d18baeb41da22bbbd8846f1ee3a789bf8835e0a281",
	"size-text/5000":        "5d9a9ee80a8617ee7642fdc489abd68d7b802967eb8295ce54ec922888facac4",
	"size-text/5001":        "c65a5002288af821cde17498553a54457f24ae45961e6b9d2197fb1e9f1c8e3b",
	"size-text/32767":       "5c42c28b9cc42d88a6377cdfa0c543d1bd59a14dd006c4ea0c89af65800e1bdb",
	"size-text/32768":       "dc0a8207e1c90a5f537bb4bb07e63cd4f5bf0f840684fb4daed7a6da6fbbc44c",
	"size-text/32769":       "8bdad0b110771ef1c805ab0bdffb19d1d02668f71b3229d2b9734a2ee943ab6d",
	"size-text/65535":       "81b10a38809add09c4f456a53a0c1af29c7d4ed42ce49e112cc869c643305a92",
	"size-text/65536":       "9013685f2911e123b76552ada4a3c8f6248b7b04d67643515dcb66a12db34e1e",
	"size-text/65537":       "e8d3402f8e117e1dae3c8e98f498901d30a1f97376fe3c2740bf63cca600f80b",
	"size-text/65534":       "76b8d8964a680a6e65184222c06325670b3645621713b5f6f08daeb8e79532da",
	"size-text/299999":      "5c9ddc892f4f83f68818cb0e8a87c1137c32b7013cd5284a202680fd3ea33f9d",
	"size-text/300000":      "fe4e3a29bce7f411576e94af148a34bcb18b5a24444f6e224365b77c57364bd4",
	"size-text/300001":      "f05201eb7438582caa2a1f5593e5665924fc2f2dfb72cb07e6fa311d8e5c908d",
	"size-text/300257":      "8a14a07725546e0ff709fcd88eef3664d566dab4128a5743bd196615ee11810f",
	"size-text/300258":      "7acc8f75bf00700cda23ee729632f2f6976cd03e948aa2dd2d258e47e7db0f7b",
	"size-text/300259":      "cfd0e3e557f3fab13e71c2a3aceeeb1929b9d133de388da71a41e322fa3836de",
	"size-text/1048576":     "732de39f35d628cbffb599d83424416b57d84cf642b85447d695a50c961a8753",
	"size-rand/510":         "1fec4317bad2e814c2e2446c2a5356e52938978d8e868d4ef3f6b64a3c5b5418",
	"size-rand/511":         "898196a4b801b7a257f19ecc2dda655c988c9753277b26dc5dd657c831b86e54",
	"size-rand/512":         "2c7c643a43aa82672857b8bd318a3a27fe2a370465b054ab33a476d0c37568aa",
	"size-rand/513":         "d53e75420bea5af3560c2281aabae0e1d0977c9cf852ad2d03d0feed1d72f65c",
	"size-rand/4095":        "889c51ada369c90efb5fefe71a7e51c9259cb2b6e23446dae21b66788fadb623",
	"size-rand/4096":        "bc8ca82171aa288ea31fdbdbfba18fa04aca462cd482d639994ee8dad69a96e3",
	"size-rand/4097":        "f3a297674001768c68f6233e9d7e4841e6fa640fac8d07bde46b4d5c0d35a189",
	"size-rand/5000":        "79210b05105fc341ce60df2f8ecf6afa56904534d39667b2cef46d27b9e84554",
	"size-rand/32768":       "0e54d7c3699a33d3705cfdacfdbe325561d1c191eaed796037125d37c5612448",
	"size-rand/65536":       "0bcbd6cf99a8a68c4932f9412128b3a2a9a6e91d6ba9eea3519d518ee1ae21d4",
	"zero":                  "3a3ed164e42500a1c5b2d0093f0a813d27dc50d038f330cc100a7e70ece2e6e4",
	"same-0x41":             "8f21b47d96859b7da3164b1db832c0ccd5bed951aaa16decb4784bda93c7c20b",
	"same-0xff":             "1bffa7ef9c11319de313a60f5ae2cbcc486e5e40ebb3e25f31b550ca8aa916b0",
	"period/1":              "5a6440f67574f9d3cc79950b5a8a11575fd13d71dc2e938fada46b4182a5aaba",
	"period/2":              "ab220d599b226c2e0a3bda73112771692b70804613fed60af07a881dc579682d",
	"period/3":              "7537da0d89359bef116052b450eda2d22d0950861bbd6cb94bad66f0c407abfc",
	"period/258":            "531095d525e118e02c2d0da8c9454dee59f59bbeb7cb2e81f4358f98c89f2935",
	"period/259":            "a3cb2d9d45bfe2601cdd80daaeea5f37228008e91c0e2b302b923c7a50dd7744",
	"runs":                  "58dd8268f46168ec2c7cf0840314e9a7abbab1092711d154bd50556c97e97de2",
	"random":                "310ea80cb637b57dd007a3c9e8a42ed3819a5d3c47ac7fc5b38fa1be90e0495b",
	"text":                  "fd24489b348bbb4ced91b2ec1c9b6992a0754967ce92d06e46b08cdf8c67ff5b",
	"alphabet/2":            "ab6964b68af2379f83c479b90676f8a902c1f40824fcfe9bb15dc08d82c9a282",
	"alphabet/4":            "4e4792c51eaaa27313d3e8a840c1754fdd8814372f6464890e6c27e896bb9246",
	"alphabet/16":           "3f9b58e963e4eda03e187c2ac2adc0ac401a798f60952c398d78de2823abd8e5",
	"alphabet-skew/5":       "a5115c7c9b3fc9adcda6a976d92236b5e84f326e12d03b6ffa9cfe322baf7eb4",
	"alphabet-skew/6":       "fd76b7b0ea470ced92d15a561110815b2aad36c0d2c4e37987952fc31bdd147c",
	"alphabet-skew/7":       "7d899877f275fb7aa4b2767cebd5519a5c1c9a92b5aba94157230fbffbe1e7b3",
	"alphabet-skew/8":       "bf68da443b107c723d24e42ed584f554b24722cbe4e33c11d54bb12b9508b765",
	"alphabet-skew/9":       "5432cb99493f7053cc9f276f46c15f2d90e6104d320ea2e7858645da3aea27d9",
	"alphabet-skew/10":      "05696ebac7f8755a355a3d2b4adff2950d7c3d6d1b0c9c1c9b44cc09437f310d",
	"alphabet-skew/12":      "7691099b5830c1fabcc8253c3de5a65bb3b3b40fcd8355592dc034199f7ce552",
	"alphabet-skew/15":      "8ebe419ec8acc3cfcf157f60ec0bf64c5f1018687785e30680aa4993327d0c77",
	"alphabet-skew/20":      "6edb0fb64191621007e611075ff1a1c5105e1bb754f67a5f543133fd7ee0a833",
	"alphabet-skew/32":      "901a05320eb9d8cbb574eab620ecd9528e5b0eabc5afa2c959659b2db22f9812",
	"alphabet-skew/44":      "781f4858ecc6a5e885a8dc12b47f5ea0fd1f308816d936eed99a09be073f5e90",
	"alphabet-skew/45":      "b4371a3d7756da1c80b1ced0dfc31d94ccd01eabaf545450ac05432217ebade5",
	"alphabet-skew/64":      "31f103230f9a6b8e514857609cbf285ba8cbfd651626ecf9072b4dee987308c4",
	"alphabet-skew/80":      "7659c20811af32c4c5030f783e1112f57d71a6ab219776fa6443d93acae361c8",
	"alphabet-skew/81":      "41e894f88f020b2b8469e70855424668afe4136043780ae9a2cb79269a1b375a",
	"narrow-prefix/4096":    "0e3ba9f1e1654d51713ae3c807602cd98900a58c10cd13bebe6b27007e5e1494",
	"narrow-prefix/10000":   "22676a2a7a8e57cc9f5a0a7e99d05555f16f221000a5652f6c8faf93d833799b",
	"records":               "9ce62d70b7835d6466d80bfd7d55750cb76edc0303c2928edf82c93a18116bc6",
	"many-short-matches":    "843235e8b43fcd38b8d995479b415616c2e4ad92ec7d77c4916935606c0428e4",
	"edge-matches":          "0cc3c627f4735900501a2664034428005be704a125042e76e8869b57d4c14dec",
	"edge-matches/65535":    "989d4e15b07bb5fe2521b75c29f8039cf5b7fb4f75bd2237463b433d97b959c7",
	"edge-matches/65537":    "0251bd6f64ce7fdfa8a966d41ad11cef33876957da6bd8163ea02172181491ab",
	"rebase-straddle/32768": "4c3331c603994bfed1c24ce35be4c0fb6a20a66c37b35bb1207d353804a0a7f2",
	"segments":              "5bee9f70b2f67a930830856ab7d8278bb1f201258c5686fc086236501f477abe",
	"litrun-stride251":      "970c814bc67832a243c05cf2f35d077183fcfa56ef27480d575b89d889d60218",
	"dense-matches":         "84cfdb54d490e41f2116a18eb342338be27bfa6b97ecec5a430c1f586cb07d71",
	"eof-same/255":          "d8f9abad0f43ffe2fce0032fd245bfc98edbdab8e222f7b6c313a35abfd2782a",
	"eof-period3/255":       "6605c363abe8fd48b8653f333e858e33876cb7c18d7f3beca6ed93f449352e29",
	"eof-same/256":          "fcc0108770388f352679507ffcf73b79716e81ff5c20f9bf5257af737d001514",
	"eof-period3/256":       "95fbc6099683cc029267a81ace02ff56ce3f5d33e9c501c1d41fc2b2f8941e19",
	"eof-same/257":          "16c04de4670e399ba23109c0d04a5426d75fe696be2ea988c4b7b1aeecc8efdc",
	"eof-period3/257":       "aec312646e31f08c3584e94da68126625ae905706958fd228fe09d52bb6d2455",
	"eof-same/258":          "98416a4805865a3a701c0e4a84cd0a9c4c0e9cc2fbdb27ddc1fdd5ac317daf23",
	"eof-period3/258":       "259185c66253315ebc2ba006278a22c153e08efd1639d3ad9b628405ea7b4e85",
	"eof-same/259":          "c60d6c75dada082eae400ebc0711c4534404b9685f426eef2e89de5505657756",
	"eof-period3/259":       "ee1507a5e97c46c0ef1eca3a108d3e0424d3957296e6b5e8ff44221af3299c95",
	"eof-same/260":          "8c04ef1ad0c66aace46e1de1af4eb68a528a39703bec2f00420e979ecdafef1e",
	"eof-period3/260":       "fb6c4fda8499cfc2bb5deb54a9c51fe09c8ba4bd344b96496138d9237b6db7c4",
	"eof-same/261":          "7fb90f1c360a5b3b51213c686c0a1e87fb8bbf68cc754569cf6f207037a9055a",
	"eof-period3/261":       "de9bfc473f45f865f573b7b332bb73505f1bb314e8f52638bee90c4f1f9aa3b5",
	"eof-same/262":          "a8844f9fb664a99aabd994befde7705dd45ae3f210b5e7d2b7aac4bd54efd68c",
	"eof-period3/262":       "a27925706d3b8bb6a71bfd6ad045a486eb7091ecb4165fdab9ecadbab458dc63",
	"eof-same/513":          "8fb3cf605b01aa9e186b02c7e64e9b30dc74cfc267c7c92c1e12e71f4eb2b990",
	"eof-period3/513":       "47f434a3e11a314ea0581681703996baa41f59c8ec544dcfa12731b69a828740",
	"eof-same/514":          "b79ff3b5049342b412c2f0cb6ef5df21d2dc5219dfb2174756fc202ba32550f2",
	"eof-period3/514":       "a5a69d808488e5a0013403dceffc89cf2a538ea7c9b4b11f2b283ceac1d19ce0",
	"eof-same/515":          "bcc32fb30b2586594e6870e931ac1f224de0e9ae77ef0834c018653f0ed62abb",
	"eof-period3/515":       "85223b2fa026edf417df3e240615f9e260cffb0fc87b1da9db16e0a8dc7c332a",
	"eof-same/516":          "82839d7b4a9aecaf803d678b0bee34c54645a0ce1faa752d89fca37486976aa6",
	"eof-period3/516":       "469ecff0e82fe404d813ced0ed11c0466178b680dcfe0a3b0bb5292acf4af98b",
	"eof-same/65792":        "1ca4432df1761d35ea8e9ca72923363bff40428eb588d67ef77d6ec70aa9b95e",
	"eof-same/65793":        "84daae044dba6980aabad1c5ff0d12fe216cdc3e3f9416321d6aad7da0d96e6d",
	"eof-same/65794":        "ff0ac88b042d2bcb8c030e9a0bd7b05fbefa263f43862b1d408f8857a11f9b59",
	"tail-copy/3+0":         "6a97ab9dbc22d03968cf3f8d6094ba00f13b1e57ce5cce75074c05de0dcbcf9e",
	"tail-copy/3+1":         "1a4b09fcbb6498bb323d951cbae5a5d0cd171d089885a6fe6e524b5866425403",
	"tail-copy/3+2":         "3601a7b6045026f5ea7a8d5b798ed45d02df70c53d3d9a637c482d11b1a28921",
	"tail-copy/4+0":         "156d0dfde4ee54103aeef1964a1ed0e8e91c577bb8711aa23f298227567eda71",
	"tail-copy/4+1":         "1d030896b88c8fd5eef655739b50177718536ebbb1a2a7d13200f01b5eef1238",
	"tail-copy/4+2":         "297bd5f99382126a9d48a11aa53ee6b0122712f33a8115963b9cfcfb2df1c6f6",
	"tail-copy/257+0":       "441231f3b85f489ace4fa06021bb96c58729ce96e4ccd25f409a9e2fc0fb5021",
	"tail-copy/257+1":       "6ecc69edfcf8e0bc9743ed9525d3f660f11c6fd3769fa5fb189d3866ef79b77b",
	"tail-copy/257+2":       "540d25f5b675ce8739241390121beffcaeb18ca2e0b387e9d53513b403f01fb4",
	"tail-copy/258+0":       "3d5ab453c395ef5caef9073304f9f922ef4f4f00bb6f738094c221811587776c",
	"tail-copy/258+1":       "23f922d075ddd9009615b3cfa6a42cb84eddf2cf59f3238129bae5200ff43030",
	"tail-copy/258+2":       "34824f2cf55ca2a362bb48d690f97a5e5e654bd2307b6fd3d4d8ff230e13838c",
	"sparse-mutations/1":    "19ac539bd353c1c70ee8b008a4e7cd7eabce1bd1b95ee25d48e4959ef5f41085",
	"sparse-mutations/2":    "6c6310223e88daeab4213af959900a58feab86b728c6449cd8dc248de77000cb",
	"sparse-mutations/3":    "1e8b15953f643ff4f84469ca8ed5c5fd610aaefeb9da1748cbb71c7b8420c3d2",
	"sparse-mutations/258":  "a946ed3ee280ec710417bd11f4ecc7dae6e5a074a44a079f61c745fa69749faa",
	"ladder-a-k-b/200":      "bc298aebb825939492ecc452f64628c43d1842e14ec1e75802b279f45bc08c32",
	"thue-morse/24000":      "9f318834bd4332649256630d55fca329a4a454fbb8c06c8e9a107beaa5abcb2a",
	"fibonacci/24000":       "909595707d907cca04a13eb28ad816100303ddfbd199dc907995fa0df5c96e6d",
	"alt-runs/24000":        "5ec5b15a79be2ca9306a46a08c726407bb2642795c2268b9e8a8fb0e11647226",
	"dense4":                "e178bdd9d8e375b48607916e503e7cd90fde70b1c6104fae85fb638f42ac7d89",
	"ht-collide/3":          "f987bd9b9fadd899adeec2c069a8346ae6139ab4f4c4a9206f5094e668760743",
	"ht-collide/8":          "9b7ca532082a57545e8e390e10ce3bd275027d533dab75740caee564d4b6e661",
	"rebase-align":          "0c6977e20f00623029e02fac2cd1eea1257fe73d9013e94cd14746807951f952",
	"dist-32768":            "b1b4f3e9ccb1ea82c915eb029bae4178b8a84053596de4ba1e72bc5ccdefba67",
	"hc-chain/8":            "e65ab0a402adbabcd80db045b41ac7b32b13c1447f876d6230bbbf338f0ef82f",
	"hc-chain/14":           "cf539ffdc8bc2efc317ab7fb7499e655a64939c7290be108878a9977e6665da3",
	"hc-chain/20":           "a91772bbbb0ec8ef887d907ed25510f8ad2238a873247a69e477bf5caf34c375",
	"hc-collide3/4":         "af02d0d5dd276f5487b88445f420a7426267ca33a0999a491a1651432768961b",
	"hc-collide4/6":         "e13e8621bc0402afdb83ce899024625858b573616d3061b3817c82b89694361f",
	"len3-offsets":          "3500a8e81b5e2173ae6ee8b3e5ed0bf87897cfc86ac95f515fed8bec4221ba73",
	"split-shift/4999":      "706bf03a5dc0213bf4006107bb12815132062595050fa79116fa352d1650d7d3",
	"split-shift/5001":      "afa5d2e845bc72b3fec57f7c7c133dff5cb4ac193b6671f0a31fbf3227f71003",
	"minlen-small/511":      "9a611bcb2dc4c5753fcf17450298399d116600d302a3b7c17dac225b978f37ad",
	"minlen-small/512":      "b3dedab89bd9a59f1ce2896fff8cdcda4f51ff9a061671c6f98c38a28eed00dc",
	"minlen-small/513":      "34d5f5f698c21316df7a8e6346d6b9f58c0fed8a903b5b6fa941b6a851043488",
	"lazy-pairs":            "f76dcdc9c8b491af8aad7f0c5e4681c013cf30817664ae47bc0e513258ffc6e7",
	"lazy-runs":             "a85ac41c2beac2edbe923bc9cae1973c5ef769e05f8bbc6797b53d7837a67bb1",
	"lazy-recalc/9990":      "b2cd2a40e0ae750df02378137fb65b0821d0d5cd8d39c37420be8458b449a878",
	"nopt-overflow/2":       "8188846702c1a5fb5aaa46ad90d55fcc1f757ec5925591a998c5eefc8e521eac",
	"zipf-text":             "bed89856094386212a5770409edcafbe950f331042b02f136f14aa41fd8402b7",
	"transitions":           "7f1a5aea6dd0cc64dc14c39887c94b189edf4facef1335f023242469e4481762",
	"far-offsets":           "f7a9c03eb50e319bb16a0bf9122afc59bc6f4dbb3279ce3f5d4544b08d260093",
	"static-small/999":      "57f26f26461d16fd552590c0080fbc5ecf6c74c672bbcc89a50b5ea01d0b868e",
	"static-small/1000":     "07d3577cc61a704e5dacff84c45e155dcb111883fd184395083f50f98ca784bd",
	"static-small/1001":     "06ac48f888989c663fd1ee5da4ed2731f3b1aca33fb9cacf521c3803aaa0148c",
	"static-small/9999":     "24fe833c603678b14955d6b1aa53fe7ba895b2ea871c87ed9a7e9584d97004c7",
	"static-small/10000":    "1cc2e2fe8ca4c5689c11d59cd1b56afe753d9107c52f8b4827fda62cf3e52140",
	"static-small/10001":    "257981b507586c17ca9f6f41e46d5da980253c772513554b30807b5dfea33815",
	"tiny/71":               "147c44d2df3655c4f24ebec03c134e953037548569256c62f435e0b31dbd637d",
	"tiny/74":               "32904cda214ea1319f173411265a1e4d4d5982e19c1b6c276f9f2e6742adf1da",
	"tiny/77":               "dd05a19e3179ecbdb563de2629a4387fad6a1255041e4fafd184d5c3249d3e0b",
	"tiny/80":               "7487d813f4ddfbe8f0415a66f0f97eef69662523da2311606f2f224469a6f938",
	"tiny/83":               "ce7e751a600c43ff5990fb4f9d121bf7d727fc693df413581a31e211894c883d",
	"tiny/86":               "5e4039fb8cc49ffc2ed3ef5740ea91212474f80512be8bbb8de0e253ff12718d",
	"tiny/89":               "0f2ebb12fc9faed5cdc7fdabed33522675ecb5ffb41f3dc2e7958c6ad3ac4d79",
	"tiny/92":               "b86e21d14ea124f298ce370808f3a7084128e006ee3deec30dd39a88e323845a",
	"tiny/95":               "3408395dc0972a8d94cec2cbd4c82dd3edcc46218f5a4e910147e89d6a8de1f8",
	"tiny/98":               "9592dad8812bb4a3f29aa418644c0d40f76d93127f2ebf359901d99d91a18578",
	"tiny/101":              "4114a3ae592090bf65859032fe8c42694783714605a546851483a71a2943b81c",
	"tiny/104":              "c4af95e4203455e5c2e97a86c0408485edeeb22f313c2bd2e465793687799c0c",
	"tiny/107":              "e87caa17b7927d1b7a691400506a94339a8c57bf0a1335591e098bd438740bec",
	"tiny/110":              "878419d81b17262aa2e8986923dcc9ddd5a9eb9680c8d5aa4f0627f798cfe246",
	"tiny/113":              "7dbeb15adca67bb95b45c318c126d525a4bb602c2a5ec2ed1505537a4f968280",
	"tiny/116":              "3427f68272aad021a055e8e09998411140e6a6c28cc04274fbf3a7a786cd2a3b",
	"tiny/119":              "6db0b873a7b61b628db126ca162f10b6b2f4dc98f42b30d0633ea876c9d6bd5f",
	"tiny/122":              "5455267df66bfb610311c0e11dcb3df0b016eeaad0e6cac0ddcdb1551ca98bb7",
	"tiny/125":              "64f67d5cf9bf731610bd10b48f8348dda1bc8e5f8052a59b9ebd2cb90152d6da",
	"tiny/128":              "aab6b0a5dea1a663e5c03d826de88bff34f68436847d2a71b9dce5e4ba1221e9",
	"tiny/131":              "c6d65bd76fe4260a3b83a116287a717f50a255cdd73ea50576de34c9ce316408",
	"tiny/134":              "0073540fd3b3d5dfc17ca251502c93c13e2aecb20dc8ad847ba5c53ff2bfc77f",
	"tiny/137":              "bb22ba04d86972d12af3056c49eb888e7a0838d669fca72ba74ff26e6d967e8d",
	"tiny/140":              "47af873b2ac5ca7a4584133a91702849747ea8a336b3c2ed9644a81b75fbe39f",
	"tiny/143":              "1648c6fc2efbdee8a932af0f05a95af6724fb0a1bd479c678bf3d8ec3bed2d9e",
	"tiny/146":              "9bec58ddb5dd85b410a1ec755a5d61bfbcdb21ea6355835a2f8c43e905e7cf81",
	"tiny/149":              "13163b0d31f7788064c839828b0752725a9dd11d1f430211e425476f12b670c6",
	"tiny/152":              "3dc509f4ffd11602d33d3cd0ca21d070ccc87ba88874b307df5089d714a833c4",
	"tiny/155":              "ad1f7edf1d76dcef6759099985edeae2d02a58ea076f09165acded4c44a0d715",
	"tiny/158":              "6a444a1487963e26ba67a5da2ef99516b91fbe4b7999184ac77ae3d8638de45f",
	"tiny/161":              "700810bace1298fa631e1ffe431f91a647768e8d98aaa496177c5e476e50b717",
	"tiny/164":              "89bfd7889335a84a4bda03e4b4cee538a8adc5fff0f6289cf5b8db3ad3dc6011",
	"tiny/167":              "d36c44dac25e9975f8ad8982eafe222f0c4b94f2e7bc0ccbd95d55ad1f3ad9ca",
	"tiny/170":              "825c2fa2ee466a4f9ce4571ff92af9422d6cbe81584f8e93da4ce28c1c0b4528",
	"tiny/173":              "01d5284c4d5435f4cd1c2578ea7d339fd70f2b99ba0a5d366b832b8bb808a821",
	"tiny/176":              "f9685331e99344f8939f7ea0b951c163986e4127ab7bc5a906e4583071531e16",
	"tiny/179":              "125b83d5cf2417c82aec975f570dc6c0d0ba98f322b3dfa9b643d402ac9d7bab",
	"tiny/182":              "35f4c484513c948c8bd7bfffd9fd6d417b3b630e645165f9861da40a01e61a8f",
	"tiny/185":              "ceed1a7a4885b26fc936846ce8d401d795d9ae819cbc55b961447c36032f46a3",
	"tiny/188":              "efee1c05275a7c34aaa2eb7b66a49923173243a11ed380aaa9c1bc46bd004f74",
	"tiny/191":              "1416d401b4a6c44bac040238592c62f9da525affbe055e97c2a847263e811a35",
	"tiny/194":              "4f99bfeba76e3d7b90689fbcc196e39f5da9096ac5ee00ff43451f3cd710a0d8",
	"tiny/197":              "c7791cd00b4d302f2a9b18b4c86379be9e219702b15957ad11e7a29087d8d5ea",
	"tiny/200":              "24931260079bd5c4140911cbf709b195569bebbe9cda38b61e8b2264bc433a49",
	"tiny/203":              "314849462f3a3a7ed2272f56be5affc1b20019f347d2b45d102369aba72f627b",
	"tiny/206":              "c660ebe20127a31eb633df94477ba733a2b35182c0f72a358e56946a354d5af4",
	"tiny/209":              "f77c8feac76c165cff2dd4cfc4f09cbae51ca36e28691c80c9d72378db2e1479",
	"tiny/212":              "79b432a3f0d72a278cccfadd1ccb40a6bc8ebde65afb612b6bc07b78429d32a8",
	"tiny/215":              "63b4c7d28b2ffa8a0ac62ce8092054d15366abd0f01e1a406ccf966370ae0a7e",
	"tiny/218":              "8227efd734a07220e073022ac0535ab0e5b94f7a480f9533bed60172c9bf4e19",
	"tiny/221":              "abee1e552550579cd2c080244b3cc46a1c245c892ac7583b16fe095a37cacec8",
	"tiny/224":              "767649afe0e4805d8564668c5448578248969efb3cf85ae50b26fd6269e44187",
	"tiny/227":              "857bf1e1e59fb2c8cfa3c8d0a449fd8748ef1c73422617ae9bf60a41d602f0ad",
	"tiny/230":              "c235fea8461a8dba56a6d2a14ad333fd5468e83027d1e6c7fc6a182a54dc72db",
	"tiny/233":              "73962e776a295c4e4d8411fa910d44e30da5a934c79db5e38e7ca0c189aaa9e3",
	"tiny/236":              "dc20484f9418f94e17248cf880b8866b37facd01de209e100f76532892b02930",
	"tiny/239":              "1bd60bc28bbc17538aebbd83da47cdbcd5834953be4931c5017c870c71331222",
	"tiny/242":              "804d8d7c52e99176a931f3aaf5f3758faf759a9d4de6e668fbde18a9455284b0",
	"tiny/245":              "20a677b083f9db474ba1409d3712c5af317abebf05bfd00bd37521479ccb12c4",
	"tiny/248":              "b828107ba8fa233da7281daf0577b8fcc31975346a2dad6b895cc7b8f855ace4",
	"tiny/251":              "716ff2a7127458f0f411187af26d0392f6a8d6ffbdfd15db357bce50de54cb18",
	"tiny/254":              "8760644f493381f60b67bf13a98bb6fa65a4a6d42a366714886c8227d38ba766",
	"tiny/257":              "72d74e1225fb985221e3d4ff955001e83ef509150fed8831d7649ecfc60d3f2c",
	"tiny/260":              "dbfc3cf608258b093a679f10339ed26bf5256903ccba0e8b103bbc33cd88b9a8",
	"tiny/263":              "3d7ec466cae314a0654aa82f1e3d214a24afa53df255cd84d8c09f98795b511a",
	"tiny/266":              "6df80339b6a7738125a09f51286bdfb924125fe232a2aefc31dbc72332140276",
	"tiny/269":              "5ed7358b6e302ad75811e2180385048d446d40aff74ad3a8f62ee50899075a0a",
	"tiny/272":              "dedda974ea90928003b07ac78a2a955a3ec1c3538ae450e77e2d9e29cc7106db",
	"tiny/275":              "1ef39b22188679b0122bc2cffe0275520fea7d5f014d759f8781b125338e7374",
	"tiny/278":              "f6fc09034e042140fa10bbecb31c011977ce96b7cc7f6e9d68feee82724c8e7f",
	"tiny/281":              "edb4cc39306a3d36da6dd7a5293fef9b8ebe69d2344b202ad4573a4191bb0c41",
	"tiny/284":              "6009d679c9a9ebe14f4034acefe2f95efdf23aa7576474bb4aaaa01827672a00",
	"tiny/287":              "8f409351d83770f0ec69d6c20290364381d86e996a24fbc05dd81336e4590dd1",
	"tiny/290":              "d5603056d6600f27f550ecdbf22fbc715ac863368621b02be5ae2b74fe9cc858",
	"tiny/293":              "19c87be913f6e6014e1ea92b363ee61f80cb1fc62e13515a6dafea6d19f11336",
	"tiny/296":              "6f5fa74661a3c47df51eb99cce07e370b77d9076b0439576d893cbc6e3932b35",
	"tiny/299":              "31f3e45a7164d0d96a4ebe214593bd4289412439cd91066d9d22ef74ec4d412b",
}

const goldenAll = "05b689bd335c5e5e06ee24e66b383f238cd4c82ec60fcf2423f0f9430493264f"

func TestPrintGolden(t *testing.T) {
	if os.Getenv("DEFLATETEST_PRINT") == "" {
		t.Skip("set DEFLATETEST_PRINT=1 to print golden values")
	}
	cs := Cases(true)
	for _, c := range cs {
		if !strings.HasPrefix(c.Name, "sweep-") {
			fmt.Printf("\t%q: %q,\n", c.Name, caseHash(c))
		}
	}
	fmt.Printf("goldenAll = %q\n", allHash(cs))
}

func TestGolden(t *testing.T) {
	cs := Cases(true)
	for _, c := range cs {
		if want, ok := goldenShort[c.Name]; ok && caseHash(c) != want {
			t.Errorf("case %q changed: sha256 %s, want %s", c.Name, caseHash(c), want)
		}
	}
	nonSweep := 0
	for _, c := range cs {
		if !strings.HasPrefix(c.Name, "sweep-") {
			nonSweep++
		}
	}
	if nonSweep != len(goldenShort) {
		t.Errorf("golden table has %d entries, generator has %d non-sweep cases", len(goldenShort), nonSweep)
	}
	if got := allHash(cs); got != goldenAll {
		t.Errorf("overall hash %s, want %s", got, goldenAll)
	}
}

func TestSizes(t *testing.T) {
	got := map[string]int{}
	total := 0
	for _, c := range Cases(true) {
		got[c.Name] = len(c.Data)
		total += len(c.Data)
	}
	t.Logf("short set: %d cases, %d bytes (%.2f MiB)", len(got), total, float64(total)/(1<<20))
	if total >= 8<<20 {
		t.Errorf("short set is %d bytes, want < 8 MiB", total)
	}
	for n := 0; n <= 70; n++ {
		for _, kind := range []string{"sweep-text", "sweep-rand"} {
			name := fmt.Sprintf("%s/%d", kind, n)
			if g, ok := got[name]; !ok || g != n {
				t.Errorf("%s: size %d (present=%v), want %d", name, g, ok, n)
			}
		}
	}
	for _, n := range []int{510, 511, 512, 513, 4095, 4096, 4097, 4999, 5000, 5001, 32767, 32768, 32769, 65534, 65535, 65536, 65537,
		299999, 300000, 300001, 300257, 300258, 300259, 1 << 20} {
		name := fmt.Sprintf("size-text/%d", n)
		if g, ok := got[name]; !ok || g != n {
			t.Errorf("%s: size %d (present=%v), want %d", name, g, ok, n)
		}
	}
	for _, name := range []string{"zero", "same-0x41", "same-0xff", "period/258", "random", "text",
		"alphabet/2", "alphabet/4", "alphabet/16", "records", "many-short-matches", "edge-matches"} {
		if got[name] != 96<<10 {
			t.Errorf("%s: size %d, want %d", name, got[name], 96<<10)
		}
	}
}

func TestContentShape(t *testing.T) {
	m := map[string][]byte{}
	for _, c := range Cases(true) {
		m[c.Name] = c.Data
	}
	if !bytes.Equal(m["zero"], make([]byte, len(m["zero"]))) {
		t.Error("zero case is not all zero")
	}
	for i, b := range m["same-0xff"] {
		if b != 0xff {
			t.Fatalf("same-0xff byte %d = %#x", i, b)
		}
	}
	d := m["period/258"]
	for i := 258; i < len(d); i++ {
		if d[i] != d[i-258] {
			t.Fatalf("period/258 breaks at %d", i)
		}
	}
	for _, name := range []string{"alphabet/2", "alphabet/4", "alphabet/16"} {
		var seen [256]bool
		n := 0
		for _, b := range m[name] {
			if !seen[b] {
				seen[b] = true
				n++
			}
		}
		if want := map[string]int{"alphabet/2": 2, "alphabet/4": 4, "alphabet/16": 16}[name]; n != want {
			t.Errorf("%s uses %d symbols, want exactly %d", name, n, want)
		}
	}
	// edge-matches: the 32768-distance 3-byte copy right after the random
	// prefix must exist.
	e := m["edge-matches"]
	if !bytes.Equal(e[40000:40003], e[40000-32768:40000-32768+3]) {
		t.Error("edge-matches: first match is not at distance 32768")
	}
}

func TestBlocks(t *testing.T) {
	data := Cases(true)[len(Cases(true))-1].Data
	for _, size := range []int{1, 7, 4096, 16 << 10, 128 << 10, len(data), len(data) + 1} {
		bs := Blocks(data, size)
		var cat []byte
		for i, b := range bs {
			if len(b) == 0 || len(b) > size || (i < len(bs)-1 && len(b) != size) {
				t.Fatalf("size %d: block %d has length %d", size, i, len(b))
			}
			cat = append(cat, b...)
		}
		if !bytes.Equal(cat, data) {
			t.Fatalf("size %d: concatenation differs", size)
		}
	}
	if got := Blocks(nil, 10); len(got) != 0 {
		t.Errorf("Blocks(nil) = %d blocks", len(got))
	}
}

func TestLevels(t *testing.T) {
	l := Levels()
	if len(l) != 13 || l[0] != 0 || l[12] != 12 {
		t.Errorf("Levels() = %v", l)
	}
}

func TestLongSetSuperset(t *testing.T) {
	if testing.Short() {
		t.Skip("long set")
	}
	short := map[string]string{}
	for _, c := range Cases(true) {
		short[c.Name] = caseHash(c)
	}
	long := Cases(false)
	total := 0
	found := 0
	for _, c := range long {
		total += len(c.Data)
		if h, ok := short[c.Name]; ok {
			found++
			if caseHash(c) != h {
				t.Errorf("case %q differs between short and long sets", c.Name)
			}
		}
	}
	if found != len(short) {
		t.Errorf("long set contains %d of %d short cases", found, len(short))
	}
	t.Logf("long set: %d cases, %d bytes (%.2f MiB)", len(long), total, float64(total)/(1<<20))
	if total >= 128<<20 {
		t.Errorf("long set is %d bytes, want < 128 MiB", total)
	}
	if len(long) <= len(short) {
		t.Error("long set adds no cases")
	}
}

func TestSilesiaLoader(t *testing.T) {
	fs := Silesia(t) // skips when the corpus is absent
	if len(fs) == 0 {
		t.Fatal("no files")
	}
	for i := 1; i < len(fs); i++ {
		if fs[i-1].Name >= fs[i].Name {
			t.Errorf("not sorted: %s, %s", fs[i-1].Name, fs[i].Name)
		}
	}
	if b := fs[0].Load(t); int64(len(b)) != fs[0].Size {
		t.Errorf("%s: loaded %d bytes, size %d", fs[0].Name, len(b), fs[0].Size)
	}
	if tar := SilesiaTar(t); tar.Size <= 0 {
		t.Errorf("silesia.tar size %d", tar.Size)
	}
}

func TestMinMatchBuckets(t *testing.T) {
	want := map[int]bool{}
	for _, k := range []int{5, 6, 7, 8, 9, 10, 12, 15, 20, 32, 44, 45, 64, 80, 81} {
		want[k] = true
	}
	hist := map[int]int{}
	for _, c := range Cases(true) {
		if len(c.Data) < 4096 {
			continue
		}
		var seen [256]bool
		n := 0
		for _, b := range c.Data[:4096] {
			if !seen[b] {
				seen[b] = true
				n++
			}
		}
		hist[n]++
		if strings.HasPrefix(c.Name, "alphabet-skew/") {
			var k int
			fmt.Sscanf(c.Name, "alphabet-skew/%d", &k)
			if n != k {
				t.Errorf("%s: %d distinct bytes in first 4096, want %d", c.Name, n, k)
			}
		}
	}
	for k := range want {
		if hist[k] == 0 {
			t.Errorf("no case has %d distinct bytes in its first 4096", k)
		}
	}
	if testing.Verbose() {
		t.Logf("distinct bytes in first 4096 -> number of cases: %v", hist)
	}
}

func TestDenseDensity(t *testing.T) {
	b, seqs := dense(rng("dense-matches"), 300000)
	if seqs < 60000 || len(b)/seqs > 5 {
		t.Errorf("dense: %d sequences in %d bytes (%.2f B/seq)", seqs, len(b), float64(len(b))/float64(seqs))
	}
	// Independent check on the produced bytes: a greedy parse with 3/4-byte
	// matches at distance >= 4 must find at least that many matches.
	found := 0
	for i := 4; i+3 <= len(b); {
		hit := false
		for d := 4; d <= 32768 && d <= i && !hit; d++ {
			hit = b[i] == b[i-d] && b[i+1] == b[i+1-d] && b[i+2] == b[i+2-d]
		}
		if hit {
			found++
			i += 3
		} else {
			i++
		}
		if found > 0 && i > 20000 {
			break
		}
	}
	if found < 3000 {
		t.Errorf("only %d matches found in the first 20000 bytes", found)
	}
}

func TestLitrun(t *testing.T) {
	b := litrun(2)
	if len(b) != 2*250*251 {
		t.Fatalf("litrun(2) has %d bytes, want %d", len(b), 2*250*251)
	}
	// libdeflate's construction: byte j = (stride*multiple)%251.
	if b[0] != 0 || b[1] != 1 || b[251] != 0 || b[252] != 2 || b[253] != 4 {
		t.Errorf("unexpected litrun prefix %v", b[:5])
	}
	if n := len(litrun(5)); n <= 300000 {
		t.Errorf("litrun(5) has %d bytes, want > 300000", n)
	}
}

// dense4 (T2.3): 5 bytes per sequence, and the 4-byte copies are really
// there: an independent greedy parse with 4-byte matches at distances
// 5..2047 finds one per sequence. 63999 sequences in 320000 bytes, so level
// 1's FAST_SEQ_STORE_LENGTH (8192) is reached every ~41000 bytes.
func TestDense4Density(t *testing.T) {
	b, seqs := dense4(rng("dense4"), 320000)
	if len(b) != 320000 || seqs != 63999 { // 8 + 63999*5 bytes, the last copy cut
		t.Fatalf("dense4: %d sequences in %d bytes", seqs, len(b))
	}
	found := 0
	for i := 9; i+4 <= 40000; {
		hit := false
		for d := 5; d <= 2047 && d <= i && !hit; d++ {
			hit = b[i] == b[i-d] && b[i+1] == b[i+1-d] && b[i+2] == b[i+2-d] && b[i+3] == b[i+3-d]
		}
		if hit {
			found++
			i += 5 // the copy, then the next literal
		} else {
			i++
		}
	}
	if found < 7900 {
		t.Errorf("only %d 4-byte matches in the first 40000 bytes, want ~8000", found)
	}
}

func TestLevel1Shapes(t *testing.T) {
	m := map[string][]byte{}
	for _, c := range Cases(true) {
		m[c.Name] = c.Data
	}
	// ht-collide: every 4-aligned group has the same ht hash; k distinct
	// groups.
	for _, k := range []int{3, 8} {
		b := m[fmt.Sprintf("ht-collide/%d", k)]
		h := htHash(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
		groups := map[string]bool{}
		for i := 0; i+4 <= len(b); i += 4 {
			v := uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24
			if htHash(v) != h {
				t.Fatalf("ht-collide/%d: group at %d has another hash", k, i)
			}
			groups[string(b[i:i+4])] = true
		}
		if len(groups) != k || len(b) != 32768 {
			t.Errorf("ht-collide/%d: %d distinct groups, %d bytes", k, len(groups), len(b))
		}
	}
	// rebase-align: the planted copies start at the planned distance
	// before each 32768 boundary.
	ra := m["rebase-align"]
	for j, pl := range [][2]int{{0, 258}, {1, 4}, {3, 4}, {4, 4}, {257, 258}, {258, 258}} {
		p := (j+1)*32768 - pl[0]
		ok := false
		for d := 300; d < 4300 && !ok; d++ {
			ok = bytes.Equal(ra[p:p+pl[1]], ra[p-d:p-d+pl[1]])
		}
		if !ok {
			t.Errorf("rebase-align: no %d-byte copy at %d", pl[1], p)
		}
	}
	// dist-32768: copies at distance exactly 32768 and 32767.
	d := m["dist-32768"]
	if len(d) != 65537 || !bytes.Equal(d[32768:49152], d[:16384]) || !bytes.Equal(d[49153:], d[49153-32767:65537-32767]) {
		t.Error("dist-32768: copies not at distance 32768 / 32767")
	}
}

// The levels 2..4 cases (T2.4) have the intended structure.
func TestGreedyShapes(t *testing.T) {
	m := map[string][]byte{}
	for _, c := range Cases(true) {
		m[c.Name] = c.Data
	}
	le := func(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24 }
	// hc-collide3: every 4-aligned 3-byte group has one hash3, 4 groups.
	b := m["hc-collide3/4"]
	groups := map[string]bool{}
	for i := 0; i+4 <= len(b); i += 4 {
		if hcHash3(le(b[i:])) != hcHash3(le(b)) {
			t.Fatalf("hc-collide3/4: group at %d has another hash3", i)
		}
		groups[string(b[i:i+3])] = true
	}
	if len(groups) != 4 || len(b) != 8<<10 {
		t.Errorf("hc-collide3/4: %d groups, %d bytes", len(groups), len(b))
	}
	// hc-collide4: every 4-aligned group has one hash4, 6 groups.
	b = m["hc-collide4/6"]
	groups = map[string]bool{}
	for i := 0; i+4 <= len(b); i += 4 {
		if hcHash4(le(b[i:])) != hcHash4(le(b)) {
			t.Fatalf("hc-collide4/6: group at %d has another hash4", i)
		}
		groups[string(b[i:i+4])] = true
	}
	if len(groups) != 6 {
		t.Errorf("hc-collide4/6: %d groups", len(groups))
	}
	// hc-chain: 44-byte records starting with WXYZ; a record's tail shares
	// a prefix with an earlier one.
	for _, d := range []int{8, 14, 20} {
		b = m[fmt.Sprintf("hc-chain/%d", d)]
		shared := 0
		for i := 0; i+44 <= len(b); i += 44 {
			if string(b[i:i+4]) != "WXYZ" {
				t.Fatalf("hc-chain/%d: record at %d", d, i)
			}
			for k := 1; k < d && i-44*k >= 0; k++ {
				if bytes.Equal(b[i+4:i+9], b[i-44*k+4:i-44*k+9]) {
					shared++
					break
				}
			}
		}
		if len(b) != 8<<10 || shared < 50 {
			t.Errorf("hc-chain/%d: %d bytes, %d records share >= 5 tail bytes", d, len(b), shared)
		}
	}
	// split-shift: 4 segments; the third is zeros.
	for _, seg := range []int{4999, 5001} {
		b = m[fmt.Sprintf("split-shift/%d", seg)]
		if len(b) != 4*seg || !bytes.Equal(b[2*seg:3*seg], make([]byte, seg)) {
			t.Errorf("split-shift/%d: %d bytes", seg, len(b))
		}
	}
	for _, n := range []int{511, 512, 513} {
		if b = m[fmt.Sprintf("minlen-small/%d", n)]; len(b) != n {
			t.Errorf("minlen-small/%d: %d bytes", n, len(b))
		}
	}
	if b = m["len3-offsets"]; len(b) != 16<<10 {
		t.Errorf("len3-offsets: %d bytes", len(b))
	}
	if testing.Short() {
		return
	}
	for _, c := range Cases(false) {
		if c.Name == "dense3-near" {
			// 4 bytes per (literal, 3-byte copy from distance 4..4095).
			found := 0
			for i := 4097; i+4 <= 40000; i += 4 {
				for d := 4; d <= 4095; d++ {
					if bytes.Equal(c.Data[i:i+3], c.Data[i-d:i-d+3]) {
						found++
						break
					}
				}
			}
			if len(c.Data) != 300000 || found < 8900 {
				t.Errorf("dense3-near: %d bytes, %d of %d copies found", len(c.Data), found, (40000-4097)/4)
			}
		}
	}
}

// longestBack is the longest match (and its distance) at b[i:] within
// maxOff bytes back, by brute force.
func longestBack(b []byte, i, maxOff int) (int, int) {
	best, dist := 0, 0
	for d := 1; d <= maxOff && d <= i; d++ {
		l := 0
		for i+l < len(b) && l < 258 && b[i+l] == b[i+l-d] {
			l++
		}
		if l > best {
			best, dist = l, d
		}
	}
	return best, dist
}

func TestLazyShapes(t *testing.T) {
	m := map[string][]byte{}
	for _, c := range Cases(true) {
		m[c.Name] = c.Data
	}
	// lazy-pairs: targets with a match at p and a different-distance match
	// (one shorter, as long or longer) at p+1 or p+2, both directions of
	// the length difference and of the offset order present.
	b := m["lazy-pairs"]
	var shorter, equal, longer, nearer, farther int
	for i := 0; i < len(b); i++ {
		l0, d0 := longestBack(b, i, 1024)
		if l0 < 3 {
			continue
		}
		for s := 1; s <= 2; s++ {
			l1, d1 := longestBack(b, i+s, 1024)
			if l1 < 3 || d1 == d0 {
				continue
			}
			switch {
			case l1 < l0:
				shorter++
			case l1 == l0:
				equal++
			default:
				longer++
			}
			if d1 < d0 {
				nearer++
			} else {
				farther++
			}
		}
		i += l0
	}
	if len(b) != 24<<10 || min(shorter, equal, longer, nearer, farther) < 5 {
		t.Errorf("lazy-pairs: %d bytes; next match shorter %d, equal %d, longer %d; nearer %d, farther %d",
			len(b), shorter, equal, longer, nearer, farther)
	}
	// lazy-runs: runs of period 1..4 of at least 29 bytes.
	b = m["lazy-runs"]
	periods := map[int]int{}
	for i := 0; i+300 < len(b); {
		l, d := longestBack(b, i+4, 4)
		if l >= 25 {
			periods[d]++
			i += 4 + l
			continue
		}
		i++
	}
	if len(b) != 12<<10 || len(periods) != 4 {
		t.Errorf("lazy-runs: %d bytes, periods %v", len(b), periods)
	}
	// lazy-recalc/9990: 6 symbols for 9990 bytes, then text.
	b = m["lazy-recalc/9990"]
	used := map[byte]bool{}
	for _, c := range b[:9990] {
		used[c] = true
	}
	if len(b) != 11990 || len(used) != 6 {
		t.Errorf("lazy-recalc/9990: %d bytes, %d symbols in the prefix", len(b), len(used))
	}
}

func TestNearOptimalShapes(t *testing.T) {
	m := map[string][]byte{}
	for _, c := range Cases(true) {
		m[c.Name] = c.Data
	}
	distinct := func(b []byte) int {
		var seen [256]bool
		n := 0
		for _, v := range b {
			if !seen[v] {
				seen[v] = true
				n++
			}
		}
		return n
	}
	if b := m["nopt-overflow/2"]; len(b) != 260000 || distinct(b) != 2 {
		t.Errorf("nopt-overflow/2: %d bytes, %d symbols", len(b), distinct(b))
	}
	// zipf-text: words of lower-case letters, the most frequent word far more
	// common than a mid-rank one.
	words := map[string]int{}
	for _, w := range strings.Fields(strings.ReplaceAll(string(m["zipf-text"]), ",", "")) {
		words[w]++
	}
	top := 0
	for _, n := range words {
		top = max(top, n)
	}
	if len(m["zipf-text"]) != 32<<10 || len(words) < 200 || top < 300 {
		t.Errorf("zipf-text: %d bytes, %d distinct words, top count %d", len(m["zipf-text"]), len(words), top)
	}
	// far-offsets: many 4-byte groups repeat at a distance 32700..32767.
	b := m["far-offsets"]
	far := 0
	for i := 32768; i+4 <= len(b); i += 2 {
		for d := 32700; d < 32768; d++ {
			if bytes.Equal(b[i:i+4], b[i-d:i-d+4]) {
				far++
				break
			}
		}
	}
	if len(b) != 48<<10 || far < 1000 {
		t.Errorf("far-offsets: %d bytes, %d far 4-byte repeats", len(b), far)
	}
	// transitions: random (about 256 symbols) and narrow segments alternate.
	b = m["transitions"]
	wide, narrow := 0, 0
	for i := 0; i+2048 <= len(b); i += 2048 {
		switch d := distinct(b[i : i+2048]); {
		case d > 200:
			wide++
		case d < 60:
			narrow++
		}
	}
	if len(b) != 64<<10 || wide < 3 || narrow < 3 {
		t.Errorf("transitions: %d bytes, %d wide and %d narrow 2 KiB chunks", len(b), wide, narrow)
	}
	for _, n := range []int{999, 1000, 1001, 9999, 10000, 10001} {
		if got := len(m[fmt.Sprintf("static-small/%d", n)]); got != n {
			t.Errorf("static-small/%d: %d bytes", n, got)
		}
	}
	for n := 71; n <= 300; n += 3 {
		if got := len(m[fmt.Sprintf("tiny/%d", n)]); got != n {
			t.Errorf("tiny/%d: %d bytes", n, got)
		}
	}
}
