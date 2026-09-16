# Decisions

1. The API can create a database and then lose the response. How does your controller avoid creating a duplicate on the next reconcile? What risk remains that you did not eliminate?

I cannot programmatically recover a lost database ID without a listing endpoint. The idea I came up with: set status to **ORPHANED_RESPONSE_LOST** and enforce user to handle this manually (delete resource and start over).
The risk I do not eliminate: database might be created on remote but isn't tracked by kuber. In this case I cannot delete it, so theoretically it would waste compute resource.

2. What would you change about this external API to make your job easier? How would you argue for it to the team that owns it and has its own backlog?

The `GET /databases` must be implemented as soon as possible to recover remote state and update local cluster. 
This way I would track all resources, even if previous `POST` failed with **InternalServerError**. Otherwise, we risk paying for compute without tracking and using it.
Considering the price, it will be unacceptable for business.

3. Deleting the external database can keep failing. Do you block deletion of the custom resource indefinitely, or give up at some point and let it go? Justify the choice you made.

The best architectural decision in this specific case: keep kubernetes trying to delete the resourse.
The architecture kubernetes is build on is state. We'd better try indefinitely, then leaving it as it is, and losing track of compute resources,
paying for them without actually using. 

4. Someone edits sizeGB after the database exists. The API has no resize operation. What does your controller do, and what does the user see?

My controller ignores the change because the reconciliation loop checks for an existing DatabaseID before processing creation logic. Since the API lacks a resize operation, no HTTP request is made. 
The user sees a discrepancy: their local YAML manifest shows the new size, but the status.sizeGB (if we tracked it) and the actual database remain unchanged. 
The cluster state and external state are now desynced, and the user receives no error or feedback explaining why.

5. What did you deliberately leave out, and what would you do next?

Considering **Explicitly not required block** here is the next steps that would promote this project from take-home to production grade:

    a. A validating admission webhook: I'd implement a webhook to reject `UPDATE` request when immutable fields of the resource changed in YAML, with explicitly returning `HTTP 400` to user,
    so `kubectl apply` returns a error instead of falling silently.
    
    b. Synchronize state with kubernetes metav1.Condition protocol: Instead of just simple `State: "PROVISIONING"` string I would implement proper standart Kubernetis conditions:
    (e.g., type: Ready, status: False, reason: Provisioning) which makes it easier for other automated tools (like ArgoCD) to understand your resource's state.

    c. Obvservability: I would add metrics about average provisioning time, count of `HTTP 500` responses, etc.

