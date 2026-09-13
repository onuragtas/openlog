# 08 — Riskler

| Risk | Etki | Önlem |
|---|---|---|
| **Cardinality patlaması** (sınırsız attribute değerleri) | ClickHouse şişer, sorgular yavaşlar, maliyet artar | Ingest'te tenant başına seri limiti; URL/SQL normalizasyonu; limit aşımı kendi metriğiyle raporlanır |
| **Tenant verisinin sızması** | SaaS için ölümcül | Tenant yalnızca license key'den; API sorgu katmanında zorunlu filtre; bu katman için ayrı test paketi ve güvenlik incelemesi |
| **Agent overhead** | Müşteri uygulaması yavaşlar | Kaynak bütçesi, CI'da benchmark, bütçe aşımında otomatik yavaşlama |
| **Envanter/keşifte hassas veri** | Parola/token sızıntısı | Komut satırı maskeleme, ortam değişkeni ve dosya içeriği gönderilmez, maskeleme kuralları için testler |
| **Keşif yanlış pozitif/negatif** | Yanlış öneri, eksik izleme | Kurallar veri; her kural için örnek envanter fixture'ı ile test; topluluk katkısı |
| **Cluster işletme karmaşıklığı** (Kafka + ClickHouse) | Self-hosted kullanıcı zorlanır | `single` profil; operator'lar; runbook'lar; yük/chaos testleri CI'da |
| **Rebalance'ta tekrar yazma** | Sayımlarda küçük sapma | Aynı aralık aynı chunk'lara bölünür ve ClickHouse dedup token'ı ile elenir (M1). Kalan dar durumlar `kafka.md`'de; hiçbiri veri kaybettirmez |
| **İptal edilen license key'in bir süre daha çalışması** | Sızan key ≤60 sn daha veri gönderebilir; Postgres kesintisinde son 15 dk görülen key'ler kabul edilir | Belgelendi (D-021); cache süresi ayarlanabilir; iptal audit log'a yazılır |
| **Üyelikten çıkarılan kullanıcının açık oturumu** | Erişim bir sonraki istekte kesilir ama oturum kaydı süresi dolana kadar kalır | Üyelik her istekte kontrol edilir; oturumların anında silinmesi M1 kalanı |
| **Kaba kuvvet giriş denemesi** | Çok IP'den dağıtılmış denemeler hesap başına sınırlanmıyor | E-posta+IP sınırı var; hesap başına küresel sınır ve CAPTCHA/SSO sonraki adım |
| **Postgres kesintisi** | Yeni oturum/yönetim işlemleri durur; API pod'ları `/readyz` ile LB dışına çıkar | Ingest cache ile çalışmaya devam eder; CNPG ile 3 instance production |
| **iCloud Drive üzerinde geliştirme** | `node_modules` dosyaları iCloud tarafından diskten çıkarılıyor; `tsc`/build dakikalarca sürüyor veya takılıyor | Build'ler yerel kopyada çalıştırılıyor; proje taşınırsa ortadan kalkar |
| **PHP agent (C/Rust extension)** | Hata müşterinin uygulamasını çökertir | Fuzz ve uzun süreli stabilite testleri, kademeli rollout |
| **Kapsamın büyüklüğü** | Hiçbir şeyin bitmemesi | Kilometre taşı bazlı dikey dilimler; her taşın sonunda kullanılabilir sürüm |
