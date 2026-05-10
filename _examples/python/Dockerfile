FROM python:3.13-slim

WORKDIR /work

COPY requirements.txt /work/requirements.txt
RUN pip install --no-cache-dir -r requirements.txt

COPY example.py /work/example.py

CMD ["python", "example.py"]
