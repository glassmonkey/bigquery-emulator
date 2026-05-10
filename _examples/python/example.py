from google.api_core.client_options import ClientOptions
from google.auth.credentials import AnonymousCredentials
from google.cloud import bigquery


def main():
    client = bigquery.Client(
        "test",
        client_options=ClientOptions(api_endpoint="http://bigquery:9050"),
        credentials=AnonymousCredentials(),
    )
    # Selecting id + name only — the emulator currently serialises
    # TIMESTAMP columns as a float-formatted string ("1666310400.0")
    # which the modern google-cloud-bigquery client rejects with
    # ValueError. Tracking the serialisation fix as a separate
    # follow-up; this example keeps the round-trip simple.
    job = client.query(
        query="SELECT id, name FROM dataset1.table_a",
        job_config=bigquery.QueryJobConfig(),
    )
    for row in job.result():
        print(row)


if __name__ == "__main__":
    main()
