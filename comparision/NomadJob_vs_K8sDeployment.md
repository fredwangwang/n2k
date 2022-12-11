

# Nomad Job Spec

The basic block for nomad is `task`. A task contains a single container and resources assciated with it. [task](https://developer.hashicorp.com/nomad/docs/job-specification/task)

Muttiple `task`s form a `group`. Tasks within the same group are guarenteed to be placed on the same node. [group](https://developer.hashicorp.com/nomad/docs/job-specification/group)

Multiple `group`s form a `job`. Job is the top-most layer in nomad. [job](https://developer.hashicorp.com/nomad/docs/job-specification/job)

Example:

```hcl
job "blog" {
    group "frontend" {
        task "nginx" {}
    }

    group "backend" {
        task "server" {}
        
        task "server-logger" {}
    }

    group "database" {
        task "db" {}
    }
}
```

The scalablity is defined at `group` level, with `count` argument to specify how many replica's to run, and nomad ensures the desired number of replicas are running.

# Kubernetes Deployment Spec

The basic block for k8s is `pod`. A pod is a group of one or more containers that are guarentted to be placed on the same node. [pod](https://kubernetes.io/docs/concepts/workloads/pods/)

Then `replicaset` layers on top of `pod` to provide horizontal scale to be able to run number of replicas as specificed. [ReplicaSet](https://kubernetes.io/docs/concepts/workloads/controllers/replicaset/)

Then `deployment` layers on top of `replicaset` to provide additional features like controlling rollout speed and versioning. [Deployment](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/)

Example:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-deployment
  labels:
    app: nginx
spec:
  replicas: 3
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:1.14.2
        ports:
        - containerPort: 80
```

# Comparision

As shown above, there is really no 1-1 mapping from the nomad constructs to k8s constructs. Fortunately, they are very similar:

Regardless of the replication count, `group` in nomad more or less maps to a `pod` in k8s;

When specifiying the `count`, `group` in nomad can be mapped to a `replicaset` in k8s;

When including the [`update`](https://developer.hashicorp.com/nomad/docs/job-specification/update) stanza, `group` in nomad can be thought of a `deployment` in k8s;

For a `job` in nomad with a single group, it is similar to a `deployment` in k8s;

For a `job` in nomad with multi groups, then it is similiar to multiple `deployment`s in k8s. K8s does not provide any builtin abstractions on top of `deployemnt`, however, it is no suprise that there is something like `helm` charts to fill in that gap.

# Conclusion

1. For `job` with a single group -> `deployment`
2. For `job` with multi groups -> `helm` chart
