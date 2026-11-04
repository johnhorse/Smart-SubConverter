{% if request.target == "clash" or request.target == "clashr" %}

port: {{ default(global.clash.http_port, "7890") }}
socks-port: {{ default(global.clash.socks_port, "7891") }}
allow-lan: {{ default(global.clash.allow_lan, "true") }}
mode: Rule
log-level: {{ default(global.clash.log_level, "info") }}
external-controller: {{ default(global.clash.external_controller, "127.0.0.1:9090") }}
{% if default(request.clash.dns, "") == "1" %}
dns:
  enable: true
  nameserver:
  - https://dns.alidns.com/dns-query#h3=true
  - https://doh.pub/dns-query
  direct-nameserver:
  - https://dns.alidns.com/dns-query#h3=true
  - https://doh.pub/dns-query
  proxy-server-nameserver:
  - https://doh.pub/dns-query
  - https://dns.alidns.com/dns-query#h3=true
  default-nameserver:
  - tls://119.29.29.29
  - tls://223.5.5.5
# Fake-IP 模式下，建议不要启用 fallback-server
{% endif %}
{% if local.clash.new_field_name == "true" %}
proxy-groups: ~
rules: ~
{% else %}
proxy-groups: ~
rules: ~
{% endif %}

{% endif %}
{% if request.target == "surge" %}

[General]
loglevel = notify
bypass-system = true
skip-proxy = 127.0.0.1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,100.64.0.0/10,localhost,*.local,e.crashlytics.com,captive.apple.com,::ffff:0:0:0:0/1,::ffff:128:0:0:0/1
#DNS设置或根据自己网络情况进行相应设置
bypass-tun = 192.168.0.0/16,10.0.0.0/8,172.16.0.0/12
dns-server = 119.29.29.29,223.5.5.5

[Script]
http-request https?:\/\/.*\.iqiyi\.com\/.*authcookie= script-path=https://raw.githubusercontent.com/NobyDa/Script/master/iQIYI-DailyBonus/iQIYI.js

{% endif %}
{% if request.target == "loon" %}

[General]
# IPV6 启动与否
ipv6 = false
# udp 类的 dns 服务器，用,隔开多个服务器，system 表示系统 dns
dns-server = 119.29.29.29, 223.5.5.5
# DNS over HTTPS服务器，用,隔开多个服务器
doh-server = https://223.5.5.5/resolve, https://sm2.doh.pub/dns-query
# 是否开启局域网代理访问
allow-wifi-access = false
proxy-test-url = https://www.gstatic.com/generate_204

[Proxy]
DIRECT = direct

{% endif %}
{% if request.target == "quanx" %}

[general]
excluded_routes=192.168.0.0/16, 172.16.0.0/12, 100.64.0.0/10, 10.0.0.0/8
network_check_url=https://www.gstatic.com/generate_204
server_check_url=https://www.gstatic.com/generate_204

[dns]
server=119.29.29.29
server=223.5.5.5
server=1.0.0.1
server=8.8.8.8

[policy]
static=🔰 节点选择, direct

[server_remote]

[server_local]

[filter_remote]

[filter_local]

[mitm]

{% endif %}
