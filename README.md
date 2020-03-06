# kubernetes配置使用统计

为了搞清楚当前kubernetes集群中控制器和配置(configmap/secret)的依赖关系

### 用法

```shell
Usage of ./kube-config-chain:
  -c string
        挑选配置项(支持正则匹配) (default ".*")
  -f string
        展示格式(支持yaml、json) (default "yaml")
  -k string
        kube配置文件 (default "/etc/kubernetes/kubelet.conf")
  -n string
        指定命名空间 (default "default")
  -s    精简输出
```

## 编译

```shell
dep ensure
go build

# 忽略符号表
# go build -ldflags "-s"
# 静态编译
# CGO_ENABLED=0 go build -a -ldflags '-extldflags "-static"' .
```

## TODO List

- 统计job/cronjob等资源使用的配置
- 统计未被使用的配置